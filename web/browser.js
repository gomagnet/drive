'use strict';
const filePages=new Set(['dashboard','files','recent','trash']);
function spaceDisplay(s){if(!s.personal)return s.name;return s.owner_id===state.user?.id?t('My files'):ui`${s.owner_name} · Personal files`;}
function storagePercent(s){return s.quota_bytes?Math.min(100,s.used_bytes/s.quota_bytes*100):(s.used_bytes?100:0);}
function fileParams(){const p=new URLSearchParams({space:state.space.id});if(state.parent)p.set('parent',state.parent);if(state.page==='trash')p.set('trash','1');if(state.page==='recent'||state.page==='dashboard')p.set('recent','1');if(state.query)p.set('q',state.query);return p;}
async function load({contentOnly=false}={}){
 hideContextMenu();if(!filePages.has(state.page))return;
 const request=++state.request;if(!state.space)return;
 if(!contentOnly)state.selected.clear();
 $('#main').setAttribute('aria-busy','true');
 if(!contentOnly)$('#main').innerHTML=ui('<div class="loading" role="status">Opening your files…</div>');
 try{
  const result=await api('/api/entries?'+fileParams());if(request!==state.request||!state.user)return;
  state.files=result.entries;sortFiles();state.selected=new Set([...state.selected].filter(id=>state.files.some(f=>f.id===id)));
  if(contentOnly&&$('#files-panel'))patchFileContent(result.limit);else render(result.limit);
 }catch(e){if(request===state.request&&state.user&&!contentOnly){$('#main').innerHTML=ui`<div class="empty"><h3>Could not open your files</h3><p>${esc(e.message)}</p><button id="retry-files">Try again</button></div>`;$('#retry-files').onclick=()=>load().catch(err=>toast(err.message));}throw e;}
 finally{if(request===state.request)$('#main')?.removeAttribute('aria-busy');}
}
async function refresh(options={}){const id=state.space?.id;const spaces=await api('/api/spaces');if(!state.user)return;state.spaces=spaces;state.space=spaces.find(s=>s.id===state.space?.id)||spaces.find(s=>s.personal&&s.owner_id===state.user.id);if(state.space&&id===state.space.id)patchStorage();if(filePages.has(state.page))await load(options);}
function patchStorage(){const s=state.space;if(!s||!$('#storage'))return;$('#storage').innerHTML=ui`<span>${esc(spaceDisplay(s))}</span><div class="meter"><i id="storage-fill"></i></div>${bytes(s.used_bytes)} of ${bytes(s.quota_bytes)} used`;$('#storage-fill').style.width=storagePercent(s)+'%';if(state.page==='dashboard'){const total=$('.stat strong');if(total)total.textContent=bytes(s.used_bytes);$$('[data-open-space]').forEach(b=>{const sp=state.spaces.find(x=>x.id===Number(b.dataset.openSpace));if(sp)$('small',b).textContent=ui`${bytes(sp.used_bytes)} used · ${roleLabel(sp.role)}`;});}}
function filePanelHTML(limit){return ui`<section class="panel" id="files-panel" aria-label="Files"><div class="toolbar"><button id="directory-up" class="icon-button" aria-label="Go to parent directory" title="Go up" ${!state.parent?'disabled':''}>${icon('up')}</button><div class="crumb" id="folder-crumb"><button data-crumb="-1">${esc(spaceDisplay(state.space))}</button>${state.trail.map((t,i)=>ui`<span>/</span><button data-crumb="${i}">${esc(t.name)}</button>`).join('')}</div><span id="selection-count"></span><div class="actions" id="bulk-actions" hidden></div><small id="file-count"></small></div><div class="table-scroll" id="file-table" ${!state.files.length?'hidden':''}><table><thead><tr><th><input id="select-all" type="checkbox" aria-label="Select all files"></th><th><button id="sort-name">Name ↕</button></th><th class="hide-mobile">Last modified</th><th class="hide-tablet">Modified by</th><th class="hide-mobile">Size</th><th>Actions</th></tr></thead><tbody id="file-rows"></tbody></table></div><div class="empty" id="file-empty" ${state.files.length?'hidden':''}>${icon(state.page==='trash'?'trash':state.query?'search':'folder')}<h3>${state.page==='trash'?t('Nothing in the trash'):state.query?t('No matching files'):t('A fresh space')}</h3><p>${state.page==='trash'?t('Your files are right where they belong.'):state.query?t('Try another name or select a different space.'):t('Drop files anywhere in this window, or choose Upload files.')}</p></div></section>`;}
function patchFileContent(limit){$('#file-table').hidden=!state.files.length;$('#file-empty').hidden=!!state.files.length;$('#file-count').textContent=ui`${state.files.length}${state.files.length===limit?ui` (first ${limit}; refine your search)`:''} item${state.files.length!==1?'s':''}`;rows();patchStorage();}
function sortFiles(){if(state.sortNames!==undefined)state.files.sort((a,b)=>Number(b.folder)-Number(a.folder)||(state.sortNames?1:-1)*a.name.localeCompare(b.name));}
function bindFilePanel(){
 patchFileContent(1000);
 $('#select-all').onchange=e=>{state.selected=e.target.checked?new Set(state.files.map(f=>f.id)):new Set();syncRowSelection();};
 $('#sort-name').onclick=()=>{state.sortNames=state.sortNames===undefined?true:!state.sortNames;sortFiles();rows();$('#sort-name').textContent=t('Name ')+(state.sortNames?'↑':'↓');};
 $('#directory-up').onclick=()=>goUp().catch(e=>toast(e.message));
 $$('[data-crumb]').forEach(b=>b.onclick=()=>{const index=Number(b.dataset.crumb);state.parent=index<0?null:state.trail[index].id;state.trail=state.trail.slice(0,index+1);state.query='';$('#search').value='';state.page='files';load().catch(e=>toast(e.message));});
 $('#files-panel').oncontextmenu=e=>{if(e.target.closest('[data-row]'))return;e.preventDefault();showContextMenu(e,null);};
}
function rows(){
 const trash=state.page==='trash';const body=$('#file-rows');if(!body)return;
 body.innerHTML=state.files.map(f=>ui`<tr data-row="${f.id}" tabindex="0" aria-label="${esc(f.name)}" aria-selected="${state.selected.has(f.id)}" class="${state.selected.has(f.id)?'selected':''}"><td><input type="checkbox" data-select="${f.id}" aria-label="Select ${esc(f.name)}" ${state.selected.has(f.id)?'checked':''}></td><td><div class="file-name"><span class="file-icon ${f.folder?'folder':''}">${icon(typeIcon(f))}</span><div><button data-open="${f.id}" title="Double-click to open ${esc(f.name)}">${esc(f.name)}</button><small>${f.folder?t('Folder'):esc(f.mime.split(';')[0])}</small></div></div></td><td class="hide-mobile">${esc(date(f.updated_at))}</td><td class="hide-tablet">${esc(f.editor)}</td><td class="hide-mobile">${f.folder?'—':bytes(f.size)}</td><td><div class="row-actions">${!f.folder&&!trash?ui`<button class="icon-button" data-download="${f.id}" title="Download" aria-label="Download ${esc(f.name)}">${icon('download')}</button>`:''}<button class="icon-button" data-details="${f.id}" title="File actions" aria-label="Actions for ${esc(f.name)}">⋯</button></div></td></tr>`).join('');
 $$('[data-row]',body).forEach(row=>{
  const f=state.files.find(f=>f.id===Number(row.dataset.row));
  row.onclick=e=>{if(e.target.closest('input,.row-actions'))return;selectRow(f.id,e);};
  row.ondblclick=e=>{if(e.target.closest('input,.row-actions'))return;e.preventDefault();openEntry(f);};
  row.oncontextmenu=e=>{e.preventDefault();e.stopPropagation();if(!state.selected.has(f.id)){state.selected=new Set([f.id]);syncRowSelection();}showContextMenu(e,f);};
  row.onkeydown=e=>{if(e.target.closest('input,.row-actions'))return;if(e.key==='Enter'){e.preventDefault();openEntry(f);}else if(e.key===' '){e.preventDefault();selectRow(f.id,{ctrlKey:true});}else if(e.key==='ContextMenu'||e.shiftKey&&e.key==='F10'){e.preventDefault();const r=row.getBoundingClientRect();if(!state.selected.has(f.id)){state.selected=new Set([f.id]);syncRowSelection();}showContextMenu({clientX:r.left+50,clientY:r.top+25},f);}};
 });
 $$('[data-select]',body).forEach(b=>b.onchange=()=>{const id=Number(b.dataset.select);b.checked?state.selected.add(id):state.selected.delete(id);syncRowSelection();});
 $$('[data-download]',body).forEach(b=>b.onclick=()=>download(Number(b.dataset.download)));
 $$('[data-details]',body).forEach(b=>b.onclick=e=>{e.preventDefault();const f=state.files.find(f=>f.id===Number(b.dataset.details));if(!state.selected.has(f.id)){state.selected=new Set([f.id]);syncRowSelection();}const r=b.getBoundingClientRect();showContextMenu({clientX:r.right,clientY:r.bottom},f);});
 selection();
}
function selectRow(id,event){if(event.shiftKey&&state.selectionAnchor){const a=state.files.findIndex(f=>f.id===state.selectionAnchor),b=state.files.findIndex(f=>f.id===id);if(a>=0){if(!event.ctrlKey&&!event.metaKey)state.selected.clear();for(const f of state.files.slice(Math.min(a,b),Math.max(a,b)+1))state.selected.add(f.id);}}else if(event.ctrlKey||event.metaKey){state.selected.has(id)?state.selected.delete(id):state.selected.add(id);state.selectionAnchor=id;}else{state.selected=new Set([id]);state.selectionAnchor=id;}syncRowSelection();}
function syncRowSelection(){$$('[data-row]').forEach(row=>{const selected=state.selected.has(Number(row.dataset.row));row.classList.toggle('selected',selected);row.setAttribute('aria-selected',String(selected));$('input',row).checked=selected;});selection();}
async function openEntry(f){try{hideContextMenu();if(state.page==='trash'){details(f);return;}if(!f.folder){await preview(f);return;}const id=state.space.id,request=++state.request;const trail=await api(`/api/entries/${f.id}/path`);if(request!==state.request||state.space.id!==id)return;state.parent=f.id;state.trail=trail;state.page='files';state.query='';$('#search').value='';await load();}catch(e){toast(e.message);}}
async function goUp(){if(!state.parent)return;const id=state.space.id,parent=state.parent,request=++state.request;const trail=await api(`/api/entries/${parent}/path`);if(request!==state.request||id!==state.space.id)return;trail.pop();state.trail=trail;state.parent=trail.at(-1)?.id||null;state.page='files';state.query='';$('#search').value='';await load();}
function hideContextMenu(){const menu=$('#file-context-menu');if(menu)menu.remove();}
function showContextMenu(event,file){
 hideContextMenu();const actions=[];const ids=[...state.selected];const writable=state.space.role!=='viewer';const single=file&&ids.length===1;
 const add=(label,glyph,run,danger=false)=>actions.push({label,glyph,run,danger});
 if(!file){if(writable&&state.page!=='trash'){add(t('Upload files'),'upload',()=>chooseUpload());add(t('New folder'),'folder',()=>$('#new-folder')?.click());}if(state.parent)add(t('Go to parent folder'),'up',()=>goUp());}
 else if(state.page==='trash'){if(writable){add(t('Restore'),'clock',()=>bulk('restore',ids));add(t('Delete forever'),'trash',()=>purgeDialog(ids),true);}if(single)add(t('Details'),'file',()=>details(file));}
 else{
  if(single){add(t('Open'),'folder',()=>openEntry(file));if(!file.folder){add(t('Download'),'download',()=>download(file.id));add(t('Version history'),'clock',()=>versionHistory(file));if(writable){add(t('Share / direct link'),'link',()=>shareDialog(file));add(t('Upload new version'),'upload',()=>chooseUpload(file.id));}}}
  if(writable){if(single)add(t('Rename'),'edit',()=>nameDialog(t('Rename'),t('Name'),async name=>{await send(`/api/entries/${file.id}`,{name},'PATCH');await load({contentOnly:true});},file.name));add(t('Move'),'move',()=>moveDialog(ids));add(t('Move to trash'),'trash',()=>confirmTrash(ids),true);}
  if(single)add(t('Details'),'file',()=>details(file));
 }
 if(!actions.length)return;
 const menu=document.createElement('div');menu.id='file-context-menu';menu.className='context-menu';menu.setAttribute('role','menu');menu.setAttribute('aria-label',t('File actions'));menu.innerHTML=actions.map((a,i)=>ui`<button role="menuitem" data-menu-action="${i}" class="${a.danger?'danger':''}">${icon(a.glyph)}${esc(a.label)}</button>`).join('');document.body.append(menu);
 menu.style.left=Math.max(8,Math.min(event.clientX,innerWidth-menu.offsetWidth-8))+'px';menu.style.top=Math.max(8,Math.min(event.clientY,innerHeight-menu.offsetHeight-8))+'px';
 $$('button',menu).forEach(b=>b.onclick=()=>{const action=actions[Number(b.dataset.menuAction)];hideContextMenu();Promise.resolve().then(()=>action.run()).catch(e=>toast(e.message));});
 menu.onkeydown=e=>{const buttons=$$('button',menu),index=buttons.indexOf(document.activeElement);if(e.key==='Escape'){hideContextMenu();$(`[data-row="${file?.id}"]`)?.focus();}else if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();buttons[(index+(e.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length].focus();}else if(e.key==='Tab')hideContextMenu();};$('button',menu).focus();
}
document.addEventListener('pointerdown',e=>{if(!e.target.closest('#file-context-menu'))hideContextMenu();});
window.addEventListener('resize',hideContextMenu);document.addEventListener('scroll',e=>{if(e.target instanceof Element&&e.target.closest('#file-context-menu'))return;hideContextMenu();},true);
let dropDepth=0;
function isFileDrag(e){return e.dataTransfer&&[...e.dataTransfer.types].includes(t('Files'));}
function hideDrop(){dropDepth=0;document.body.classList.remove('drop-active');$('#window-drop-zone')?.remove();}
window.addEventListener('dragenter',e=>{if(!isFileDrag(e))return;e.preventDefault();if(!state.user)return;dropDepth++;document.body.classList.add('drop-active');if(!$('#window-drop-zone')){const overlay=document.createElement('div');overlay.id='window-drop-zone';overlay.innerHTML=ui`${icon('upload')}<strong>Drop files to upload</strong><span>${esc(state.space?spaceDisplay(state.space):t('Your space'))}</span>`;document.body.append(overlay);}});
window.addEventListener('dragover',e=>{if(isFileDrag(e)){e.preventDefault();e.dataTransfer.dropEffect=state.user&&state.space?.role!=='viewer'&&state.page!=='trash'?'copy':'none';}});
window.addEventListener('dragleave',e=>{if(isFileDrag(e)&&--dropDepth<=0)hideDrop();});
window.addEventListener('drop',e=>{if(!isFileDrag(e))return;e.preventDefault();hideDrop();if(!state.user||!state.space||state.space.role==='viewer'||state.page==='trash'){toast(t('Open a writable space to upload files'));return;}if([...e.dataTransfer.items].some(i=>i.webkitGetAsEntry?.()?.isDirectory)){toast(t('Drop files rather than directories'));return;}replaceID=null;uploadFiles([...e.dataTransfer.files]).catch(err=>toast(err.message));});
window.addEventListener('dragend',hideDrop);window.addEventListener('blur',hideDrop);
