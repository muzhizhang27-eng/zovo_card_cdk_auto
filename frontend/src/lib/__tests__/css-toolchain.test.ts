import { expect, it } from 'vitest'
import postcss from 'postcss'
import tailwindcss from 'tailwindcss'
import nested from 'postcss-nested'

it('preserves nested selectors, responsive variants and arbitrary selectors with the patched parser', async () => {
  const result = await postcss([
    nested(),
    tailwindcss({
      darkMode: 'class',
      content: [{ raw: '<div class="hover:text-red-500 md:grid-cols-2 dark:bg-black [&>span]:text-sm"></div>', extension: 'html' }],
    }),
  ]).process('@tailwind utilities; .outer { & > .inner { color: red; } }', { from: undefined })
  const rules: string[] = [], media: string[] = []
  result.root.walkRules(rule => { rules.push(rule.selector) })
  result.root.walkAtRules('media', rule => { media.push(rule.params) })
  expect(rules).toContain('.outer > .inner')
  expect(rules).toContain('.hover\\:text-red-500:hover')
  expect(rules).toContain('.md\\:grid-cols-2')
  expect(rules.some(selector => selector.includes('dark\\:bg-black') && selector.includes('.dark'))).toBe(true)
  expect(rules.some(selector => selector.includes('text-sm') && selector.endsWith('>span'))).toBe(true)
  expect(media).toContain('(min-width: 768px)')
})
