const $=id=>document.getElementById(id);
const selectedJobs=new Set(),selectedEpisodes=new Set(),selectedAudios=new Set(),selectedSubtitles=new Set();
let snapshot=null,lastJobs='',lastAnalysis='',browserStamp='',showAll=false,manageKeys=[],toastTimer,analyzing=false,preferredAudio=[];
let lastBrowserCapture='',pendingBrowserCapture=null,importingBrowserCapture=false;
const icon=name=>'<svg class="icon" aria-hidden="true"><use href="#i-'+name+'"/></svg>';
const language=value=>{try{return new Intl.DisplayNames(['es'],{type:'language'}).of(value)}catch{return value}};
function notify(text,error=false){$('toast').textContent=text;$('toast').className='toast visible'+(error?' error':'');clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('toast').className='toast',5500)}
async function call(action,data={}){try{return await native(action,data)}catch(e){notify(String(e),true);throw e}}
function fire(action,data={}){call(action,data).catch(()=>{})}
function page(name){document.querySelectorAll('.page').forEach(x=>x.classList.toggle('active',x.id==='page-'+name));document.querySelectorAll('.nav').forEach(x=>x.classList.toggle('active',x.dataset.page===name));$('view-name').textContent=({netflix:'Preparar descarga',browser:'Navegador',downloads:'Descargas',completed:'Biblioteca local',settings:'Ajustes'})[name]||''}
document.querySelectorAll('[data-page]').forEach(b=>b.onclick=()=>page(b.dataset.page));
document.querySelectorAll('.new-download').forEach(b=>b.onclick=()=>{page('netflix');$('url').focus()});
['open-folder','footer-folder'].forEach(id=>$(id).onclick=()=>fire('folder'));
function options(select,values,label){select.replaceChildren();values.forEach(value=>{const option=document.createElement('option');option.value=value;option.textContent=label(value);select.append(option)})}
function validAnalysis(){const a=snapshot?.analysis;return !!a&&!a.busy&&!a.error&&a.source===$('url').value.trim()&&a.heights?.length>0&&a.audio?.length>0}
function clearPrepared(){selectedEpisodes.clear();selectedAudios.clear();selectedSubtitles.clear();$('prepared-panel').hidden=true;$('welcome').hidden=false;updatePlan()}
$('url').oninput=()=>{$('clear-url').hidden=!$('url').value;if(snapshot?.analysis?.source!==$('url').value.trim()){lastAnalysis='';clearPrepared();$('analysis-feedback').className='feedback';$('analysis-feedback').textContent=$('url').value?'Pulsa Analizar enlace para preparar este contenido.':''}};
$('clear-url').onclick=()=>{$('url').value='';$('url').dispatchEvent(new Event('input'));$('url').focus()};
async function analyzeLink(){page('netflix');analyzing=true;let accepted=false;$('analyze-button').disabled=true;$('analyze-button').textContent='Analizando…';clearPrepared();try{await call('analyze',{URL:$('url').value.trim()});accepted=true;await refresh()}catch{}finally{analyzing=false;$('analyze-button').disabled=!!snapshot?.analysis?.busy}return accepted}
$('analyze-form').onsubmit=e=>{e.preventDefault();analyzeLink()};
function prepareLink(address,analyze=false){$('url').value=address;$('url').dispatchEvent(new Event('input'));page('netflix');if(analyze)analyzeLink();else $('url').focus()}
$('browser-form').onsubmit=async e=>{e.preventDefault();try{await call('browser',{URL:$('browser-url').value});notify('Navegador abierto con tu sesión local, si está disponible.')}catch{}};
$('analyze-browser-link').onclick=async()=>{try{const c=await call('browser-link');lastBrowserCapture=c.at;pendingBrowserCapture=c;await importBrowserCapture()}catch{}};
$('session-form').onsubmit=async e=>{e.preventDefault();try{await call('session',{Normal:$('normal').value,Secure:$('secure').value});$('normal').value='';$('secure').value='';notify('Sesión guardada y protegida en Windows.');await refresh()}catch{}};
$('settings-form').onsubmit=async e=>{e.preventDefault();try{await call('settings',{output:$('output').value,cdm:$('cdm').value,ffmpeg:$('ffmpeg').value,seriesFoldersByName:$('series-folders-by-name').checked,independentAudioMP4:$('independent-audio-mp4').checked});['output','cdm','ffmpeg'].forEach(id=>delete $(id).dataset.edited);notify('Ajustes guardados.');await refresh()}catch{}};
['output','cdm','ffmpeg'].forEach(id=>$(id).oninput=()=>$(id).dataset.edited='true');
function languageChoices(target,values,selected,onChange){target.replaceChildren();values.forEach(value=>{const label=document.createElement('label');label.className='language-option';const check=document.createElement('input');check.type='checkbox';check.value=value;check.checked=selected.has(value);check.onchange=()=>{check.checked?selected.add(value):selected.delete(value);onChange()};const name=document.createElement('span');name.textContent=language(value);label.append(check,name);target.append(label)})}
function renderAnalysis(a){
  const busy=analyzing||!!a?.busy;$('analyze-button').disabled=busy;$('analyze-button').textContent=busy?'Analizando…':'Analizar enlace';
  const stamp=JSON.stringify(a);if(stamp===lastAnalysis)return;lastAnalysis=stamp;
  const matches=a?.source===$('url').value.trim(),feedback=$('analysis-feedback');
  feedback.className='feedback'+(matches&&a.error?' error':'');feedback.textContent=busy?'Consultando el contenido y los idiomas disponibles…':matches?a.error||'':'';
  if(!matches||a.busy||!a.id){clearPrepared();return}
  const series=a.kind==='series'&&a.series?.seasons?.length;
  $('prepared-panel').hidden=false;$('welcome').hidden=true;$('inline-series').hidden=!series;$('episode-names').hidden=!!series;
  $('title').disabled=$('series-name-input').disabled=!!series;$('title').value=a.title||'';$('series-name-input').value=a.seriesName||a.title||'';
  $('content-title').textContent=a.title||'Contenido de Netflix';$('content-type').textContent=series?'Serie':a.kind==='movie'?'Película':a.kind==='episode'?'Episodio':'Sin clasificar';
  const count=series?a.series.seasons.reduce((n,s)=>n+(s.episodes?.length||0),0):1;
  $('content-summary').textContent=series?a.series.seasons.length+' '+(a.series.seasons.length===1?'temporada':'temporadas')+' · '+count+' episodios. Selecciona los que quieres descargar.':'Elige los idiomas de audio y los subtítulos para este episodio.';
  $('language-note').hidden=!series;
  selectedEpisodes.clear();
  if(series){options($('season-select'),a.series.seasons.map(s=>String(s.id)),id=>a.series.seasons.find(s=>String(s.id)===id).title);$('episode-filter').value='';renderEpisodes()}else selectedEpisodes.add(a.id);
  selectedAudios.clear();const audios=a.audio||[],preferred=preferredAudio.filter(v=>audios.includes(v));
  (preferred.length?preferred:[audios.includes('es-ES')?'es-ES':audios.includes('es')?'es':audios[0]]).filter(Boolean).forEach(v=>selectedAudios.add(v));
  selectedSubtitles.clear();const subtitles=a.subtitleLanguages||[];subtitles.filter(v=>v==='es-ES'||v==='es').forEach(v=>selectedSubtitles.add(v));if(!selectedSubtitles.size&&subtitles[0])selectedSubtitles.add(subtitles[0]);
  languageChoices($('audio-options'),audios,selectedAudios,()=>{preferredAudio=[...selectedAudios];updatePlan()});languageChoices($('subtitle-options'),subtitles,selectedSubtitles,updatePlan);
  options($('quality'),a.heights||[],v=>v+'p');if((a.heights||[]).includes(540))$('quality').value='540';
  updatePlan();
}
function visibleEpisodes(){const a=snapshot?.analysis,season=a?.series?.seasons?.find(s=>String(s.id)===$('season-select').value);if(!season)return[];const filter=$('episode-filter').value.trim().toLocaleLowerCase('es');return(season.episodes||[]).filter(e=>!filter||e.title.toLocaleLowerCase('es').includes(filter)||String(e.number)===filter)}
function episodeToolbar(){const visible=visibleEpisodes(),selected=visible.filter(e=>selectedEpisodes.has(e.id)).length;$('select-episodes').disabled=!visible.length;$('select-episodes').checked=visible.length>0&&selected===visible.length;$('select-episodes').indeterminate=selected>0&&selected<visible.length;$('episode-count').textContent=selectedEpisodes.size+' '+(selectedEpisodes.size===1?'episodio seleccionado':'episodios seleccionados')}
function renderEpisodes(){
 const list=$('episode-list');list.replaceChildren();const a=snapshot?.analysis,season=a?.series?.seasons?.find(s=>String(s.id)===$('season-select').value);if(!season)return;
 const items=visibleEpisodes();if(!items.length){const text=document.createElement('p');text.className='hint';text.textContent='No hay episodios que coincidan con este filtro.';list.append(text)}
 items.forEach(e=>{const row=document.createElement('article');row.className='episode'+(selectedEpisodes.has(e.id)?' selected':'');const label=document.createElement('label');const check=document.createElement('input');check.className='episode-check';check.type='checkbox';check.checked=selectedEpisodes.has(e.id);check.onchange=()=>{check.checked?selectedEpisodes.add(e.id):selectedEpisodes.delete(e.id);row.classList.toggle('selected',check.checked);episodeToolbar();updatePlan()};const number=document.createElement('span');number.className='episode-number';number.textContent=e.number;const body=document.createElement('div');body.className='episode-body';const title=document.createElement('div');title.className='episode-title';title.textContent=e.title;const meta=document.createElement('div');meta.className='hint';meta.textContent='S'+String(season.number).padStart(2,'0')+'E'+String(e.number).padStart(2,'0')+(e.seconds?' · '+Math.round(e.seconds/60)+' min':'');body.append(title,meta);label.append(check,number,body);const actions=document.createElement('div');actions.className='episode-actions';const open=document.createElement('button');open.type='button';open.className='plain';open.textContent='Ver en Netflix';open.onclick=()=>fire('browser',{URL:'https://www.netflix.com/watch/'+e.id});actions.append(open);row.append(label,actions);list.append(row)});episodeToolbar();updatePlan();
}
$('season-select').onchange=()=>{$('episode-filter').value='';renderEpisodes()};$('episode-filter').oninput=renderEpisodes;
$('select-episodes').onchange=e=>{visibleEpisodes().forEach(ep=>e.target.checked?selectedEpisodes.add(ep.id):selectedEpisodes.delete(ep.id));renderEpisodes()};
$('clear-episodes').onclick=()=>{selectedEpisodes.clear();renderEpisodes()};
function updatePlan(){
 const mode=$('subtitle-mode').value;$('subtitle-options').hidden=mode!=='selected';
 const episodes=selectedEpisodes.size,audios=selectedAudios.size,total=episodes*audios;
 const valid=validAnalysis()&&total>0&&total<=128&&(mode!=='selected'||selectedSubtitles.size>0);
 $('enqueue').disabled=!valid;$('enqueue').textContent=total?'Añadir '+episodes+' '+(episodes===1?'descarga':'descargas'):'Añadir a descargas';
 $('queue-summary').textContent=total?episodes+' '+(episodes===1?'episodio':'episodios')+' × '+audios+' '+(audios===1?'audio':'audios')+' · '+(audios===1?'Un solo idioma: se omite la comparación de fotogramas.':$('independent-audio-mp4').checked?'Un MP4 completo por idioma.':'Vídeo compartido si coincide; audio por separado.'):'Selecciona episodios y audio para continuar.';
 if(total>128)$('queue-summary').textContent='Selecciona un máximo de 128 combinaciones por vez.';
 if(mode==='selected'&&!selectedSubtitles.size)$('queue-summary').textContent='Elige al menos un idioma de subtítulos o selecciona Sin subtítulos.';
 const a=snapshot?.analysis;
 $('file-preview').textContent=a?.kind==='movie'?'Películas → '+($('title').value||a.title||'Película')+' · Vídeo, subtítulos, ficha e ilustraciones disponibles.':a?.kind==='series'||a?.metadata?.numberingKnown?($('series-folders-by-name').checked?'Nombre de serie/S01/S01E01':'Series/Temporada/Episodio')+' · Vídeos, audios, subtítulos, ficha e ilustraciones disponibles.':'La descarga quedará pendiente de clasificación hasta confirmar su identidad.';

}
$('subtitle-mode').onchange=updatePlan;$('quality').onchange=updatePlan;['title','series-name-input'].forEach(id=>$(id).oninput=updatePlan);
$('series-folders-by-name').onchange=updatePlan;$('independent-audio-mp4').onchange=updatePlan;
$('preparation-form').onsubmit=async e=>{e.preventDefault();if(!validAnalysis())return;const a=snapshot.analysis;try{const result=await call('enqueue',{URL:$('url').value.trim(),ID:a.id,Title:$('title').value,Series:$('series-name-input').value,EpisodeIDs:[...selectedEpisodes],AudioLanguages:[...selectedAudios],Height:Number($('quality').value),SubtitleMode:$('subtitle-mode').value,SubtitleLanguages:$('subtitle-mode').value==='selected'?[...selectedSubtitles]:[]});notify(result.queued+' descargas añadidas'+(result.skipped?' · '+result.skipped+' ya existentes o en cola.':'.'));if(result.queued)page('downloads');await refresh()}catch{}};
async function importBrowserCapture(){if(importingBrowserCapture||analyzing||snapshot?.analysis?.busy||!pendingBrowserCapture)return;const capture=pendingBrowserCapture;pendingBrowserCapture=null;importingBrowserCapture=true;try{prepareLink(capture.url);const accepted=await analyzeLink();if(accepted){await native('browser-link-ack',{At:capture.at});notify('Enlace recibido del navegador. Preparando episodios e idiomas.')}else if(!pendingBrowserCapture)pendingBrowserCapture=capture}catch(error){notify('No se pudo confirmar el enlace recibido: '+String(error),true)}finally{importingBrowserCapture=false}}
function receiveBrowserCapture(capture){if(capture?.url&&capture.at&&!capture.consumed&&capture.at!==lastBrowserCapture){lastBrowserCapture=capture.at;pendingBrowserCapture=capture}importBrowserCapture()}
function renderBrowser(){const capture=snapshot?.browser||{},stamp=JSON.stringify(capture);if(stamp===browserStamp)return;browserStamp=stamp;$('analyze-browser-link').disabled=!capture.url;$('browser-feedback').textContent=capture.url?'Último enlace enviado: '+capture.url:'Todavía no has enviado ningún enlace.'}
function empty(list,complete){const div=document.createElement('div');div.className='empty';div.innerHTML=icon(complete?'folder':'download');const h=document.createElement('h2');h.textContent=complete?'Tus vídeos terminados aparecerán aquí':'No hay descargas en curso';const p=document.createElement('p');p.textContent=complete?'Cada episodio tendrá sus vídeos, audios, subtítulos y catálogo de pistas.':'Pega el enlace de una serie o un episodio para preparar una descarga.';const button=document.createElement('button');button.textContent='Nueva descarga';button.onclick=()=>{page('netflix');$('url').focus()};div.append(h,p,button);list.append(div)}
$('tab-active').onclick=()=>{showAll=false;renderJobs()};$('tab-all').onclick=()=>{showAll=true;renderJobs()};
async function refresh(){try{snapshot=await native('snapshot',{});$('session-note').textContent=snapshot.session?'Sesión guardada':'Sin sesión guardada';$('session-status').textContent=snapshot.session?'Sesión guardada en este PC':'Sin sesión guardada';$('session-dot').classList.toggle('ok',snapshot.session);$('output-summary').textContent=snapshot.settings.output;$('format-audio-mode').textContent=snapshot.settings.independentAudioMP4?'MP4 independiente por idioma':'Vídeo compartido si coincide';$('footer-folder').title=snapshot.settings.output;$('engine-status').textContent=snapshot.ready?'Motor preparado':'Revisa los ajustes del motor';$('dependency-status').textContent=snapshot.problems?.join(' ')||'Los archivos del motor, FFmpeg y FFprobe están disponibles.';['output','cdm','ffmpeg'].forEach(id=>{if(document.activeElement!==$(id)&&!$(id).dataset.edited)$(id).value=snapshot.settings[id]});if(document.activeElement!==$('series-folders-by-name'))$('series-folders-by-name').checked=!!snapshot.settings.seriesFoldersByName;if(document.activeElement!==$('independent-audio-mp4'))$('independent-audio-mp4').checked=!!snapshot.settings.independentAudioMP4;renderAnalysis(snapshot.analysis);renderBrowser();renderJobs();receiveBrowserCapture(snapshot.browser)}catch{$('engine-status').textContent='No se pudo conectar con la aplicación'}}

function updateSelection(){
  const jobs=(snapshot?.jobs||[]).filter(j=>j.state==='complete'&&!j.subtitleBusy),keys=new Set(jobs.map(j=>j.key));
  for(const key of selectedJobs)if(!keys.has(key))selectedJobs.delete(key);
  $('selection-summary').textContent=selectedJobs.size+' seleccionados';
  $('select-completed').checked=jobs.length>0&&selectedJobs.size===jobs.length;
  $('select-completed').indeterminate=selectedJobs.size>0&&selectedJobs.size<jobs.length;
  $('select-completed').disabled=!jobs.length;
  $('forget-selected').disabled=$('trash-selected').disabled=!selectedJobs.size;
}
$('select-completed').onchange=e=>{selectedJobs.clear();if(e.target.checked)(snapshot?.jobs||[]).filter(j=>j.state==='complete'&&!j.subtitleBusy).forEach(j=>selectedJobs.add(j.key));lastJobs='';renderJobs()};
$('forget-selected').onclick=()=>openManage([...selectedJobs],'history');
$('trash-selected').onclick=()=>openManage([...selectedJobs],'trash');
function openManage(keys,mode='trash'){
  manageKeys=keys.slice();const jobs=(snapshot?.jobs||[]).filter(j=>keys.includes(j.key));if(!jobs.length)return;
  $('manage-description').textContent=jobs.length===1?jobs[0].title:jobs.length+' entradas seleccionadas.';
  $('manage-error').textContent='';document.querySelector('#manage-form input[value="'+mode+'"]').checked=true;
  manageLabel();$('manage-dialog').showModal();$('manage-cancel').focus();
}
function manageLabel(){$('manage-confirm').textContent=document.querySelector('input[name="manage-mode"]:checked').value==='trash'?'Enviar a Papelera':'Quitar del historial'}
document.querySelectorAll('input[name="manage-mode"]').forEach(x=>x.onchange=manageLabel);
$('manage-cancel').onclick=()=>$('manage-dialog').close();
$('manage-form').onsubmit=async e=>{
  e.preventDefault();const buttons=[...$('manage-form').querySelectorAll('button,input')];buttons.forEach(x=>x.disabled=true);
  const mode=document.querySelector('input[name="manage-mode"]:checked').value;
  try{const result=await native('manage',{Keys:manageKeys,Mode:mode});manageKeys.forEach(k=>selectedJobs.delete(k));$('manage-dialog').close();notify(mode==='trash'?result.files+' archivos enviados a la Papelera.':result.entries+' entradas quitadas del historial.');lastJobs='';await refresh()}
  catch(error){$('manage-error').textContent=String(error)}finally{buttons.forEach(x=>x.disabled=false)}
};

function renderJobs(){
  if(!snapshot)return;
  updateSelection();
  const stamp=JSON.stringify([snapshot.jobs,showAll]);if(stamp===lastJobs)return;lastJobs=stamp;
  const jobs=snapshot.jobs||[],isTerminal=j=>['complete','error','cancelled'].includes(j.state),active=jobs.filter(j=>!isTerminal(j));
  $('active-count').textContent=active.length;$('complete-count').textContent=jobs.filter(j=>j.state==='complete').length;
  $('tab-active').classList.toggle('selected',!showAll);$('tab-all').classList.toggle('selected',showAll);
  const groups=[[$('download-list'),showAll?jobs.filter(j=>j.state!=='complete'):active,false],[$('complete-list'),jobs.filter(j=>j.state==='complete'),true]];
  groups.forEach(([list,items,complete])=>{
    list.replaceChildren();if(!items.length){empty(list,complete);return}
    items.slice().reverse().forEach(j=>{
      const row=document.createElement('article');row.className='job '+j.state;
      if(complete){const check=document.createElement('input');check.type='checkbox';check.className='job-select';check.checked=selectedJobs.has(j.key);check.disabled=!!j.subtitleBusy;check.setAttribute('aria-label','Seleccionar '+j.title);check.onchange=()=>{check.checked?selectedJobs.add(j.key):selectedJobs.delete(j.key);updateSelection()};row.append(check)}
      const mark=document.createElement('div');mark.className='job-mark';mark.innerHTML=icon(j.state==='complete'?'check':'play');
      const body=document.createElement('div');body.className='job-body';
      const title=document.createElement('div');title.className='job-title';if(j.series){const collection=document.createElement('span');collection.className='series-line';collection.textContent=j.series;title.append(collection)}title.append(document.createTextNode(j.title));
      const meta=document.createElement('div');meta.className='job-meta';
      const audioNames=(j.multiAudio?j.audioLanguages||[]:[j.audio]).filter(Boolean).map(language).join(', ');
      const size=j.bytes?' · '+new Intl.NumberFormat('es-ES',{maximumFractionDigits:2}).format(j.bytes/(j.bytes>=1073741824?1073741824:1048576))+(j.bytes>=1073741824?' GB':' MB'):'';
      meta.textContent=j.height+'p · '+audioNames+size;
      const detail=document.createElement('div');detail.className='job-detail';
      const warnings=j.exportWarnings||[];
      detail.textContent=complete?(warnings.length?'Revisa la exportación: '+warnings.join('; '):'Completado'+(j.multiAudio?' · '+(j.independentAudioMP4?(j.videoFiles?.length||0)+' MP4 completos por idioma':(j.videoFiles?.length||0)+((j.videoFiles?.length||0)===1?' vídeo · ':' vídeos · ')+(j.audioFiles?.length||0)+' audios separados'):'')+(j.subtitles?.length?' · '+j.subtitles.length+' subtítulos':'')):j.detail;
      detail.classList.toggle('warning',complete&&warnings.length>0);body.append(title,meta);
      if(!isTerminal(j)){const bar=document.createElement('div');bar.className='progress';bar.setAttribute('role','progressbar');bar.setAttribute('aria-valuenow',j.progress);bar.setAttribute('aria-valuemin',0);bar.setAttribute('aria-valuemax',100);bar.setAttribute('aria-label','Progreso de '+j.title);const fill=document.createElement('div');fill.style.width=j.progress+'%';bar.append(fill);body.append(bar)}
      body.append(detail);const actions=document.createElement('div');actions.className='job-actions';
      let maintenance=actions;
      if(j.state==='complete'){
        const folder=document.createElement('button');folder.className='open-files';folder.innerHTML=icon('folder')+' Archivos';folder.title='Abrir la carpeta con todos los archivos de la descarga';folder.setAttribute('aria-label','Abrir carpeta de '+j.title);folder.onclick=()=>fire('open',{Key:j.key,Folder:true});
        const play=document.createElement('button');play.innerHTML=icon('play')+(j.multiAudio&&!j.independentAudioMP4?' Vídeo sin audio':' Reproducir');if(j.multiAudio&&!j.independentAudioMP4)play.title='Abrir el vídeo sin audio; las pistas de idioma están en Audios';play.onclick=()=>fire('open',{Key:j.key,Folder:false});actions.append(folder,play);
        if(j.multiAudio){const audios=document.createElement('button');audios.textContent=j.independentAudioMP4?'MP4 por idioma':'Audios';audios.title=j.independentAudioMP4?'Abrir los MP4 completos organizados por idioma':'Abrir las pistas de audio por idioma';audios.onclick=()=>fire('open',{Key:j.key,Asset:'audios'});actions.append(audios)}
        const more=document.createElement('details');more.className='job-more';const summary=document.createElement('summary');summary.textContent='Más opciones';summary.setAttribute('aria-label','Más opciones de '+j.title);
        maintenance=document.createElement('div');maintenance.className='job-maintenance';
        const info=document.createElement('p');info.className='job-detail';info.textContent=j.detail;maintenance.append(info);
        if(j.libraryVersion===2){const metadata=document.createElement('button');metadata.textContent=j.subtitleBusy?'Preparando…':'Actualizar ficha';metadata.disabled=!!j.subtitleBusy;metadata.title='Actualizar fichas e ilustraciones para MediaCenter';metadata.onclick=()=>fire('metadata',{Key:j.key});maintenance.append(metadata)}
        const subs=document.createElement('button');subs.textContent=j.subtitleBusy?'Guardando…':'Guardar subtítulos';subs.disabled=!!j.subtitleBusy;subs.title='Guardar los subtítulos elegidos para esta descarga';subs.onclick=async()=>{try{await call('subtitles',{Key:j.key});await refresh()}catch{}};maintenance.append(subs);more.append(summary,maintenance);actions.append(more);
      }
      if(isTerminal(j)){
        const remove=document.createElement('button');remove.className='remove-job';remove.disabled=!!j.subtitleBusy;remove.innerHTML=icon('trash');remove.title='Gestionar archivos';remove.setAttribute('aria-label','Eliminar o quitar del historial: '+j.title);remove.onclick=()=>openManage([j.key]);if(complete)remove.append(document.createTextNode(' Gestionar archivos'));maintenance.append(remove);
      }else{const cancel=document.createElement('button');cancel.textContent='Cancelar';cancel.onclick=()=>call('cancel',{Key:j.key});actions.append(cancel)}
      body.append(actions);row.append(mark,body);list.append(row);
    });
  });
};

refresh();setInterval(refresh,1000);
