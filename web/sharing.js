'use strict';
function expiryLabel(value){return value?t('Expires ')+date(value):t('Never expires');}
async function shareDialog(f){
 const epoch=modal(t('Public links'),ui('<p class="hint">Loading links…</p>'));
 const links=await api(`/api/entries/${f.id}/shares`);if(epoch!==modalEpoch)return;
 $('#modal-body').innerHTML=ui`<p class="hint">Anyone with a link can access the current file version. Preview links open a viewer; direct links serve the file itself. Forever links remain valid until revoked or the file is trashed. URLs are shown only when created.</p><div id="share-list">${links.map(l=>ui`<div class="share-row"><div>Link #${l.id}<small>${esc(expiryLabel(l.expires_at))}</small></div><button class="danger" data-revoke="${l.id}">Revoke</button></div>`).join('')}</div><form id="share-form"><label>Access time<select name="days"><option value="7">7 days</option><option value="1">1 day</option><option value="30">30 days</option><option value="365">1 year</option><option value="0">Forever</option></select></label><label>Link type<select name="type"><option value="preview">Preview page</option><option value="direct">Direct file link</option></select></label><p class="error" role="alert"></p><div class="form-actions"><button type="submit" class="primary">Create link</button></div></form><div class="link-result" id="link-result"></div>`;
 $$('[data-revoke]').forEach(b=>b.onclick=async()=>{b.disabled=true;try{await api(`/api/shares/${b.dataset.revoke}`,{method:'DELETE'});b.closest('.share-row').remove();toast(t('Link revoked'));}catch(e){toast(e.message);b.disabled=false;}});
 bindForm('#share-form',async data=>{
  const link=await send(`/api/entries/${f.id}/shares`,{days:Number(data.get('days')),direct:data.get('type')==='direct'});
  if(epoch!==modalEpoch)return;
  $('#link-result').innerHTML=ui`<label>Your ${data.get('type')==='direct'?t('direct file'):t('preview')} link<input id="share-url" readonly value="${esc(link.url)}"></label><button id="copy-link">${icon('link')}Copy link</button><p class="hint">${esc(expiryLabel(link.expires_at))}. Anyone with the link can access this file.</p><details><summary>Other link formats</summary><label>Preview page<input readonly value="${esc(link.preview_url)}"></label><label>Direct file<input readonly value="${esc(link.direct_url)}"></label><label>Force download<input readonly value="${esc(link.direct_url+'?download=1')}"></label></details>`;
  $('#copy-link').onclick=async()=>{try{await navigator.clipboard.writeText(link.url);toast(t('Link copied'));}catch{$('#share-url').select();toast(t('Select and copy the link above'));}};
 });
}
