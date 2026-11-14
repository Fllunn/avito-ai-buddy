import fs from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';
const files=['README.md','docs/architecture.md','docs/execution-routes.md','docs/design/README.md','docs/design/buddy-experience.md','docs/adr/0011-execution-journal.md','docs/verification/redesign/README.md','docs/verification/redesign/acceptance-review.md','docs/verification/redesign/checkpoint.md'];
const missing=[];let checked=0;
for(const file of files){const text=await fs.readFile(file,'utf8');for(const match of text.matchAll(/\[[^\]]*\]\(([^)]+)\)/g)){const target=match[1].split('#')[0];if(!target||/^[a-z]+:/i.test(target))continue;checked++;try{await fs.access(path.resolve(path.dirname(file),decodeURIComponent(target)));}catch{missing.push({file,target});}}}
await fs.writeFile('docs/verification/redesign/links.json',JSON.stringify({checked,missing},null,2)+'\n');assert.equal(missing.length,0,JSON.stringify(missing));console.log(`${checked} local links checked`);
