// Package plansync 每3分钟从卡台同步逻辑套餐状态和实体产品列表。
package plansync

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/background"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const syncInterval = 3 * time.Minute

// SyncResult 同步结果摘要。
type SyncResult struct {
	Plans    int
	Products int
}

// Start 启动后台产品状态同步（goroutine；ctx.Done() 时优雅退出）。
func Start(ctx context.Context) {
	background.Go(func() { run(ctx) })
}

func run(ctx context.Context) {
	// 启动时立即同步一次
	syncOnce(ctx)
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("[plan-sync] stopped")
			return
		case <-ticker.C:
			syncOnce(ctx)
		}
	}
}

// SyncNow 供 handler 主动触发（同步调用，有 ctx 超时保护）。
func SyncNow(ctx context.Context) (SyncResult, error) {
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return doSync(ctx2)
}

func syncOnce(ctx context.Context) {
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := doSync(ctx2)
	if err != nil {
		log.Printf("[plan-sync] error: %v", err)
		return
	}
	log.Printf("[plan-sync] synced %d plans, %d products", r.Plans, r.Products)
}

func doSync(ctx context.Context) (SyncResult, error) {
	cfg := cardplatform.LoadConfig()
	if cfg.APIKey == "" {
		return SyncResult{}, nil // 未配置 API Key，静默跳过
	}
	cli := cardplatform.New(cfg)
	var res SyncResult

	// 1. 同步逻辑套餐。
	// ★只同步可卖档位★：卡台透传的是 ACC 的整张定价表，里面有 claude_*（本系统
	// 没有兑换流程）。全量写进 plan_status_cache 的话，选卡规则页会冒出一堆
	// 根本配不了卡的档位，运营还得去猜哪些是能用的。
	plans, err := cli.GetPlans(ctx)
	if err != nil {
		return res, err
	}
	for _, p := range plans.SellablePlans() {
		if err := db.UpsertPlanStatus(p.Key, p.Label, true, p.ServiceFeeUsdMinor); err != nil {
			log.Printf("[plan-sync] upsert plan %s: %v", p.Key, err)
		} else {
			res.Plans++
		}
	}

	// 2. 同步实体产品。优先 /gpt-direct/card-products（只含已上架，暂停开卡的标不可用），
	// 老卡台没有该接口时回退 /products（只含可开）。列表外的缓存删除，不保留下架卡 BIN。
	if n, err := syncDirectCardProducts(ctx, cli); err == nil {
		res.Products = n
		return res, nil
	} else {
		log.Printf("[plan-sync] GetDirectCardProducts fallback to /products: %v", err)
	}
	products, err := cli.GetProducts(ctx)
	if err != nil {
		log.Printf("[plan-sync] GetProducts error: %v", err)
		return res, nil
	}
	present := make(map[string]bool, len(products))
	for _, p := range products {
		code := p.ProductCode
		if code == "" {
			continue
		}
		present[code] = true
		cp := db.CardProductCache{
			ProductCode: code,
			Issuer:      p.Issuer,
			BIN:         p.BIN,
			Network:     p.Network,
			IssuingArea: p.IssuingArea,
			Scene:       p.Scene,
			CardGroup:   p.CardGroup,
			Description: p.Description,
			BinHeads:    p.BinHeads,
			Enabled:     true,
			SuspendedAt: p.SuspendedAt,
		}
		if err := db.UpsertCardProduct(cp); err != nil {
			log.Printf("[plan-sync] upsert product %s: %v", code, err)
		} else {
			res.Products++
		}
	}
	// 3. 本次未返回的历史缓存 → 标已下线（如全部 VISA 已从卡台下架）
	if off, err := db.PruneCardProductsExcept(present); err != nil {
		log.Printf("[plan-sync] prune products: %v", err)
	} else if off > 0 {
		log.Printf("[plan-sync] removed %d delisted products (not in openable list)", off)
	}
	return res, nil
}

func syncDirectCardProducts(ctx context.Context, cli *cardplatform.Client) (int, error) {
	items, err := cli.GetDirectCardProducts(ctx)
	if err != nil {
		return 0, err
	}
	present := make(map[string]bool, len(items))
	n := 0
	for _, p := range items {
		code := strings.TrimSpace(p.ProductCode)
		if code == "" {
			continue
		}
		present[code] = true
		suspendedAt := ""
		if p.Suspended && strings.TrimSpace(p.SuspendReason) != "" {
			suspendedAt = p.SuspendReason
		} else if p.Suspended {
			suspendedAt = "suspended"
		}
		cp := db.CardProductCache{
			ProductCode: code,
			Issuer:      p.Issuer,
			BIN:         p.BIN,
			Description: p.Label,
			Enabled:     p.Usable,
			SuspendedAt: suspendedAt,
		}
		if err := db.UpsertCardProduct(cp); err != nil {
			log.Printf("[plan-sync] upsert direct product %s: %v", code, err)
			continue
		}
		n++
	}
	if off, err := db.PruneCardProductsExcept(present); err != nil {
		log.Printf("[plan-sync] prune products: %v", err)
	} else if off > 0 {
		log.Printf("[plan-sync] removed %d delisted products (not in card-products)", off)
	}
	return n, nil
}
