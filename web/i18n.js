'use strict';
// Translate source UI strings before inserting interpolated values. Filenames,
// account/space names, URLs, editor text, and form values never enter the catalog.
const i18n=(()=>{
 let preference='auto',ru=Object.create(null);
 try{const saved=localStorage.getItem('md-language');if(['auto','en','ru'].includes(saved))preference=saved;}catch{}
 const effective=()=>preference==='auto'?((navigator.languages?.[0]||navigator.language||'en').toLowerCase().startsWith('ru')?'ru':'en'):preference;
 const marker=/\uE000(\d+)\uE001/g;
 function phrase(source){
  if(effective()!=='ru')return source;
  const edge=source.match(/^(\s*)(.*?)(\s*)$/s);if(!edge[2])return source;
  const slots=[];
  const key=edge[2].replace(marker,(_,id)=>{let index=slots.indexOf(id);if(index<0){index=slots.length;slots.push(id);}return '{'+index+'}';});
  let result=ru[key];
  // Icons are HTML interpolations preceding a label; translate only the label.
  if(result===undefined){const icon=key.match(/^((?:\{\d+\})+\s*)(.+)$/s);if(icon&&ru[icon[2]]!==undefined)result=icon[1]+ru[icon[2]];}
  if(result===undefined){const decorated=key.match(/^([←→↓↑]\s*)?(.+?)(\s*[←→↓↑↕])?$/s);if(decorated&&ru[decorated[2]]!==undefined)result=(decorated[1]||'')+ru[decorated[2]]+(decorated[3]||'');}
  if(result===undefined)return source;
  return edge[1]+result.replace(/\{(\d+)\}/g,(_,id)=>slots[Number(id)]===undefined?'':('\uE000'+slots[Number(id)]+'\uE001'))+edge[3];
 }
 function html(source){
  return source.split(/(<[^>]*>)/g).map(part=>{
   if(part.startsWith('<'))return part.replace(/\b(title|placeholder|aria-label)="([^"]*)"/g,(full,key,value)=>{const translated=phrase(value);return translated===value?full:key+'="'+translated.replace(/&/g,'&amp;').replace(/"/g,'&quot;')+'"';});
   const translated=phrase(part);return translated===part?part:translated.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
  }).join('');
 }
 function render(strings,...values){
  let skeleton=typeof strings==='string'?strings:strings.reduce((s,part,index)=>s+part+(index<values.length?'\uE000'+index+'\uE001':''),'');
  skeleton=/<\/?[a-z]/i.test(skeleton)?html(skeleton):phrase(skeleton);
  return skeleton.replace(marker,(_,id)=>String(values[Number(id)]??''));
 }
 const original=new WeakMap();
 function translateStatic(){document.documentElement.lang=effective();for(const element of document.querySelectorAll('[data-i18n]')){if(!original.has(element))original.set(element,element.textContent);element.textContent=phrase(original.get(element));}for(const element of document.querySelectorAll('[data-i18n-aria]'))element.setAttribute('aria-label',phrase(element.dataset.i18nAria));}
 function select(value){preference=['auto','en','ru'].includes(value)?value:'auto';try{localStorage.setItem('md-language',preference);}catch{}translateStatic();}
 const ready=fetch('/i18n-ru.json',{credentials:'same-origin',cache:'no-store'}).then(response=>{if(!response.ok)throw new Error('Could not load translations. Please reload this page.');return response.json();}).then(catalog=>{ru=Object.assign(Object.create(null),catalog);translateStatic();});
 window.addEventListener('languagechange',()=>{if(preference==='auto'){translateStatic();document.dispatchEvent(new Event('md-system-language'));}});
 document.documentElement.lang=effective();
 return {ready,text:phrase,render,select,translateStatic,get preference(){return preference;},get locale(){return effective();}};
})();
function t(source){return i18n.text(source);}
function ui(strings,...values){return i18n.render(strings,...values);}
function roleLabel(role){return t({owner:'Owner',editor:'Editor',viewer:'Viewer'}[role]||role);}
function themeLabel(mode){return t({auto:'System',light:'Light',dark:'Dark'}[mode]||'System');}
