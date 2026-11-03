const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const base = path.join(__dirname, '../internal/web/frontend');
const context = { window: { markdownit: require(path.join(base, 'vendor/markdown-it.min.js')) } };
vm.createContext(context);
vm.runInContext(fs.readFileSync(path.join(base, 'markdown.js'), 'utf8'), context);
const render = context.renderMarkdown;

test('renders headings, lists, emphasis, code and tables', () => {
  const html = render('# Heading\n\n- **bold** and `code`\n- next\n\n```js\nconst x = "<tag>";\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |');
  for (const expected of ['<h1>Heading</h1>', '<ul>', '<strong>bold</strong>', '<code>code</code>', 'class="language-js"', '&lt;tag&gt;', '<table>', '<td>2</td>']) {
    assert.ok(html.includes(expected), expected);
  }
});

test('escapes raw HTML and blocks executable URLs', () => {
  const html = render('<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n[x](javascript:alert%281%29)\n[x](jav&#x61;script:alert%281%29)\n![x](data:image/svg+xml;base64,PHN2Zz4=)');
  assert.ok(!html.includes('<script'));
  assert.ok(!html.includes('<img'));
  assert.ok(!html.includes('href="javascript:'));
  assert.ok(html.includes('&lt;script&gt;'));
});

test('links open separately with opener isolation, and plain newlines survive', () => {
  const html = render('[link](https://example.com)\nnext line');
  assert.ok(html.includes('target="_blank"'));
  assert.ok(html.includes('rel="noopener noreferrer"'));
  assert.ok(html.includes('<br>'));
  assert.equal(render(null), '');
});
