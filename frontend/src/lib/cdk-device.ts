export function cdkDevice(): string {
  const key = 'cdk_device_id'
  let device = localStorage.getItem(key)
  if (!device) {
    device = crypto.randomUUID()
    localStorage.setItem(key, device)
  }
  return device
}
