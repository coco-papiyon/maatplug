const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../internal/web/frontend/app.js'), 'utf8');
const context = {};
vm.createContext(context);
vm.runInContext(source.match(/function escapeHTML\(s\) \{[\s\S]*?\n\}/)[0], context);
vm.runInContext(source.slice(source.indexOf('function matchesUserName('), source.indexOf('function sortableHeader(')), context);

test('username filtering is case insensitive, partial and compatible with unset names', () => {
  assert.equal(context.matchesUserName({dataAreaUserName: 'Alice'}, ' LI '), true);
  assert.equal(context.matchesUserName({dataAreaUserName: 'Alice'}, 'Bob'), false);
  assert.equal(context.matchesUserName({}, ''), true);
  assert.equal(context.matchesUserName({}, 'Alice'), false);
});

test('area selector renders default, other and all areas with escaped metadata', () => {
  const html = context.identityFiltersHTML('session', [
    {id:'default', name:'My PC', userName:'Alice'},
    {id:'remote', name:'<img src=x onerror=alert(1)>', userName:'"Bob"'},
  ], 'default');
  assert.ok(html.includes('value="default" selected'));
  assert.ok(html.includes('value=""'));
  assert.ok(html.includes('My PC'));
  assert.ok(html.includes('Alice'));
  assert.ok(html.includes('&lt;img'));
  assert.ok(!html.includes('<img'));
  assert.ok(html.includes('&quot;Bob&quot;'));
});
