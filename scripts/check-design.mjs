import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const css=await fs.readFile('frontend/src/components/tokens.css','utf8');
const tokens=Object.fromEntries([...css.matchAll(/--([a-z-]+):\s*(#[0-9a-f]{3,6})/g)].map(m=>[m[1],m[2]]));
function luminance(hex){let raw=hex.slice(1);if(raw.length===3)raw=[...raw].map(c=>c+c).join('');const c=[0,2,4].map(i=>parseInt(raw.slice(i,i+2),16)/255).map(x=>x<=.04045?x/12.92:((x+.055)/1.055)**2.4);return c[0]*.2126+c[1]*.7152+c[2]*.0722;}
function contrast(a,b){const x=luminance(a),y=luminance(b);return(Math.max(x,y)+.05)/(Math.min(x,y)+.05);}
const checks=[['text-primary','surface-page',4.5],['text-secondary','surface-card',4.5],['text-secondary','surface-soft',4.5],['surface-card','action-primary',4.5],['status-success','surface-card',4.5],['status-warning','surface-card',4.5],['status-error','surface-card',4.5],['focus-ring','surface-card',3],['border-control','surface-card',3]].map(([foreground,background,min])=>{const ratio=contrast(tokens[foreground],tokens[background]);assert(ratio>=min,`${foreground}: ${ratio}`);return{foreground,background,ratio:Number(ratio.toFixed(2)),minimum:min};});
await fs.mkdir('docs/verification/redesign',{recursive:true});await fs.writeFile('docs/verification/redesign/contrast.json',JSON.stringify({formula:'WCAG relative sRGB luminance',checks},null,2));console.log(checks);
