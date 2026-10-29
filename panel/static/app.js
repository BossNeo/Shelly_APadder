'use strict';
const $ = id => document.getElementById(id);
let csrf = '', state = null, polling = null, toastTimer;
const statuses = {verified:'Потвърдено',pending:'За проверка',error:'Грешка'};
function toast(message, error=false) {
  $('toast').textContent=message; $('toast').className='toast'+(error?' error':''); $('toast').hidden=false;
  clearTimeout(toastTimer); toastTimer=setTimeout(()=>$('toast').hidden=true,7000);
}
async function api(path, body) {
  const options={credentials:'same-origin',headers:{}};
  if (body!==undefined) {options.method='POST';options.headers={'Content-Type':'application/json','X-CSRF-Token':csrf};options.body=JSON.stringify(body);}
  const response=await fetch('/api/'+path,options), data=await response.json();
  if(!response.ok) {if(response.status===401)showLogin();throw new Error(data.error||'Операцията не успя.');}
  return data;
}
function node(tag,text,cls) {const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;}
function showLogin() {$('login').hidden=false;$('workspace').hidden=true;$('logout').hidden=true;if(polling){clearTimeout(polling);polling=null;}}
function capacity(value) {const n=Number(value);return Number.isFinite(n)?Math.round(n/1048576)+' MB':'—';}
function busy(on) {const blocked=on||!state?.allow_changes||!!state?.recovery||!!state?.warning&&$('demo-badge').hidden;for(const id of ['new-project','all-on','all-off','scan','provision'])$(id).disabled=blocked;for(const b of document.querySelectorAll('.toggle'))b.disabled=blocked;$('refresh').disabled=on;$('target').disabled=on;$('stop').hidden=!on;$('mode').textContent=on?'Добавяне':'Рутер';$('mode-detail').textContent=on?'Временно използване на 2.4 GHz':'Нормална работа на мрежите';}
function render() {
  $('login').hidden=true;$('workspace').hidden=false;$('logout').hidden=false;
  const warning=[state.warning,!state.allow_changes?'Панелът е само за преглед. Включването, създаването на мрежи и сканирането още не са разрешени.':''].filter(Boolean).join(' ');
  $('warning').hidden=!warning;$('warning').textContent=warning;$('recovery').hidden=!state.recovery;
  $('ram').textContent=capacity(state.resource['free-memory']);$('storage').textContent=capacity(state.resource['free-hdd-space'])+' свободно място';
  $('router-detail').textContent=state.resource['board-name']+' • RouterOS '+state.resource.version;
  const managed=state.interfaces.filter(i=>i.managed), protectedNet=state.interfaces.find(i=>i.protected);
  $('protected-name').textContent=protectedNet?protectedNet.ssid+' • '+protectedNet.name+' • Не се променя от панела.':'Защитеният интерфейс още не е настроен.';
  $('net-count').textContent=managed.filter(i=>i.enabled).length+' / '+managed.length;$('project-count').textContent=state.projects.length;
  $('network-list').replaceChildren();
  for(const network of managed) {
    const project=state.projects.find(p=>p.interface===network.name), row=node('div',undefined,'network-row'), meta=node('div',undefined,'network-meta');
    row.append(node('span','⌁','network-icon'));meta.append(node('strong',project?.title||network.ssid||network.name),node('p',network.ssid+' · '+network.name+' · 2.4 GHz'));
    const toggle=node('button',undefined,'toggle');toggle.type='button';toggle.setAttribute('role','switch');toggle.setAttribute('aria-checked',network.enabled);toggle.setAttribute('aria-label',(network.enabled?'Изключи ':'Включи ')+(project?.title||network.name));
    toggle.addEventListener('click',()=>action(async()=>{await api('switch',{interfaces:[network.name],enabled:!network.enabled});await refresh();}));
    row.append(meta,node('span',network.enabled?'Включена':'Изключена','network-status'+(network.enabled?' on':'')),toggle);$('network-list').append(row);
  }
  if(!managed.length)$('network-list').append(node('p','Все още няма разрешени работни мрежи. Създай нов обект или добави съществуващите интерфейси в конфигурацията.','empty'));
  const selected=$('target').value;$('target').replaceChildren(new Option('Избери обект…',''));
  for(const project of state.projects){const net=managed.find(n=>n.name===project.interface);$('target').add(new Option(project.title+' · '+project.ssid+(net?.enabled?'':' (изключена)'),project.id));}
  if(state.projects.some(p=>p.id===selected))$('target').value=selected;
  const existing=$('existing');existing.replaceChildren(new Option('Създай нова Wi-Fi мрежа',''));
  for(const net of managed.filter(n=>!state.projects.some(p=>p.interface===n.name)))existing.add(new Option(net.ssid+' · '+net.name,net.name));
  $('history-list').replaceChildren();
  for(const item of [...state.history].reverse().slice(0,20)){const row=node('div',undefined,'history-row');row.append(node('span',item.ssid+' · '+(item.project||'')),node('time',new Date(item.time*1000).toLocaleString('bg-BG')),node('span',statuses[item.state]||item.state,'badge '+(item.state==='verified'?'green':'amber')));$('history-list').append(row);}
  if(!state.history.length)$('history-list').append(node('p','Все още няма операции. Резултатите от добавянето ще се появят тук.','empty'));
  renderJob(state.job);busy(state.job.state==='running');
}
function renderJob(job) {
  $('job-message').textContent=job.message;
  const checked=new Set([...document.querySelectorAll('[name=device]:checked')].map(n=>n.value));
  $('devices').replaceChildren();
  for(const device of job.found||[]){const row=node('div',undefined,'device-row'), label=node('label',undefined,'check'), input=node('input');input.type='checkbox';input.name='device';input.value=device.ssid;input.checked=checked.has(device.ssid);input.disabled=job.state==='running';label.append(input,node('span',device.ssid));row.append(label,node('small',device.signal+' dBm'));$('devices').append(row);}
  if(!job.found?.length){const p=node('p','Все още няма открити устройства.','empty');p.append(node('br'),node('span','Включи Shelly устройствата във фабричен AP режим и стартирай сканиране.'));$('devices').append(p);}
  $('results').replaceChildren();
  for(const r of job.results||[])$('results').append(node('div',r.ssid+' · '+(statuses[r.state]||r.state)+(r.ip?' · '+r.ip:'')+(r.message?' — '+r.message:'')+(r.ap_disabled?' · AP изключен':''),'result'));
}
async function refresh() {state=await api('state');render();if(state.job.state==='running')poll();}
function poll(){clearTimeout(polling);polling=setTimeout(async()=>{try{const job=await api('job');renderJob(job);busy(job.state==='running');if(job.state==='running')poll();else{await refresh();if(job.state==='error')toast(job.message,true);}}catch(e){toast(e.message,true);}},1200);}
async function action(fn){try{await fn();}catch(e){toast(e.message,true);}}
async function start(kind){
  if(!$('ack').checked)throw new Error('Потвърди временната пауза на 2.4 GHz и използвай 5 GHz или кабел.');
  const body={acknowledge_disconnect:true};
  if(kind==='provision'){body.project=$('target').value;body.devices=[...document.querySelectorAll('[name=device]:checked')].map(n=>n.value);body.rename=$('rename').checked;body.disable_ap=$('disable-ap').checked;if(!body.project||!body.devices.length)throw new Error('Избери обект и поне едно открито устройство.');}
  await api(kind,body);busy(true);poll();
}
$('login-form').addEventListener('submit',e=>{e.preventDefault();action(async()=>{const data=await api('login',{password:$('login-password').value});csrf=data.csrf;$('login-password').value='';await refresh();});});
$('logout').onclick=()=>action(async()=>{await api('logout',{});csrf='';showLogin();});
$('refresh').onclick=()=>action(refresh);
$('scan').onclick=()=>action(()=>start('scan'));$('provision').onclick=()=>action(()=>start('provision'));
$('stop').onclick=()=>action(async()=>{await api('stop',{});toast('Спиране… Текущата заявка може да отнеме до 20 секунди.');});
$('recover').onclick=()=>action(async()=>{await api('recover',{});await refresh();});
for(const [id,on]of [['all-on',true],['all-off',false]])$(id).onclick=()=>action(async()=>{await api('switch',{interfaces:state.interfaces.filter(n=>n.managed).map(n=>n.name),enabled:on});await refresh();});
$('new-project').onclick=()=>$('project-dialog').showModal();$('close-dialog').onclick=()=>$('project-dialog').close();
$('existing').onchange=()=>{const net=state.interfaces.find(n=>n.name===$('existing').value);if(net)$('project-form').elements.ssid.value=net.ssid;};
$('project-form').addEventListener('submit',e=>{e.preventDefault();action(async()=>{const body=Object.fromEntries(new FormData(e.target));body.existing_interface=$('existing').value;await api('projects',body);$('project-dialog').close();e.target.reset();await refresh();toast('Обектът е запазен.');});});
action(async()=>{const session=await api('session');$('demo-badge').hidden=!session.demo;$('demo-login').hidden=!session.demo;csrf=session.csrf||'';if(session.authenticated)await refresh();else showLogin();});
