const state = {
  disks: [],
  selected: null,
  scan: null,
  events: null,
  repair: null,
  repairEvents: null,
  capabilities: null,
};

const els = {
  status: document.querySelector("#serviceStatus"),
  popoutBtn: document.querySelector("#popoutBtn"),
  diskList: document.querySelector("#diskList"),
  selectedName: document.querySelector("#selectedName"),
  refreshBtn: document.querySelector("#refreshBtn"),
  startBtn: document.querySelector("#startBtn"),
  stopBtn: document.querySelector("#stopBtn"),
  blockSize: document.querySelector("#blockSize"),
  progressText: document.querySelector("#progressText"),
  progressBar: document.querySelector("#progressBar"),
  badBlocks: document.querySelector("#badBlocks"),
  elapsedText: document.querySelector("#elapsedText"),
  errRead: document.querySelector("#errRead"),
  errWrite: document.querySelector("#errWrite"),
  errCompare: document.querySelector("#errCompare"),
  scanStatus: document.querySelector("#scanStatus"),
  surface: document.querySelector("#surface"),
  log: document.querySelector("#log"),
  repairTarget: document.querySelector("#repairTarget"),
  repairBtn: document.querySelector("#repairBtn"),
  repairStopBtn: document.querySelector("#repairStopBtn"),
  repairMode: document.querySelector("#repairMode"),
  repairMargin: document.querySelector("#repairMargin"),
  repairMountpoint: document.querySelector("#repairMountpoint"),
  repairDryRun: document.querySelector("#repairDryRun"),
  repairConfirm: document.querySelector("#repairConfirm"),
  repairBadBlocks: document.querySelector("#repairBadBlocks"),
  repairRanges: document.querySelector("#repairRanges"),
  repairProgress: document.querySelector("#repairProgress"),
  repairResult: document.querySelector("#repairResult"),
  repairStatus: document.querySelector("#repairStatus"),
  repairImprovement: document.querySelector("#repairImprovement"),
  smartTable: document.querySelector("#smartTable"),
  advice: document.querySelector("#advice"),
  repairLog: document.querySelector("#repairLog"),
};

function setStats(scan) {
  els.elapsedText.textContent = scan.elapsed || "0:00";
  els.errRead.textContent = scan.errRead || 0;
  els.errWrite.textContent = scan.errWrite || 0;
  els.errCompare.textContent = scan.errCompare || 0;
  els.badBlocks.textContent = String(scan.badBlocks || 0);
}

function createSurface() {
  els.surface.innerHTML = "";
  for (let i = 0; i < 500; i += 1) {
    const cell = document.createElement("div");
    cell.className = "cell";
    els.surface.appendChild(cell);
  }
}

function setProgress(value, badBlocks = 0) {
  const progress = Math.max(0, Math.min(100, Number(value) || 0));
  els.progressText.textContent = `${progress.toFixed(progress >= 10 ? 1 : 2)}%`;
  els.progressBar.style.width = `${progress}%`;
  const cells = [...els.surface.children];
  const done = Math.floor((progress / 100) * cells.length);
  cells.forEach((cell, index) => {
    cell.className = "cell";
    if (index < done) cell.classList.add("done");
    if (index === done && progress > 0 && progress < 100) cell.classList.add("active");
  });
  for (let i = 0; i < Math.min(badBlocks, cells.length); i += 1) {
    cells[(i * 37) % cells.length].className = "cell bad";
  }
}

// 桌面以 iframe 型入口经系统网关（/app/badblocks-victoria）反代时，
// 所有接口请求必须带上该前缀，否则会打到桌面根路径导致 404。
const API_PREFIX = location.pathname.indexOf("/app/badblocks-victoria") === 0
  ? "/app/badblocks-victoria"
  : "";

function apiPath(path) {
  return API_PREFIX + path;
}

async function fetchJSON(url, options) {
  const res = await fetch(apiPath(url), options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

async function loadDisks() {
  els.status.textContent = "读取中";
  els.diskList.innerHTML = "<div class=\"disk\"><span>正在读取系统硬盘...</span></div>";
  try {
    const data = await fetchJSON("/api/disks");
    state.disks = data.disks || [];
    els.status.textContent = data.badblocksAvailable ? "badblocks 可用" : "未找到 badblocks";
    renderDisks();
  } catch (err) {
    els.status.textContent = "服务异常";
    els.diskList.innerHTML = `<div class="disk"><span>${escapeHTML(err.message)}</span></div>`;
  }
}

function renderDisks() {
  if (!state.disks.length) {
    els.diskList.innerHTML = "<div class=\"disk\"><span>没有检测到硬盘</span></div>";
    return;
  }
  els.diskList.innerHTML = "";
  state.disks.forEach((disk) => {
    const btn = document.createElement("button");
    btn.className = `disk ${state.selected?.path === disk.path ? "active" : ""}`;
    btn.innerHTML = `
      <strong>${escapeHTML(disk.path)} · ${escapeHTML(disk.sizeHuman)}</strong>
      <span>${escapeHTML(disk.model || "Unknown")} ${escapeHTML(disk.serial || "")}</span>
      <span>${disk.rotational ? "HDD" : "SSD/NVMe"} · ${escapeHTML(disk.transport || "-")} · ${escapeHTML(mountSummary(disk.mountpoint))}${(disk.holders || []).length ? " · 阵列/卷成员" : ""}</span>
    `;
    btn.addEventListener("click", () => selectDisk(disk));
    els.diskList.appendChild(btn);
  });
}

function selectDisk(disk) {
  state.selected = disk;
  els.selectedName.textContent = `${disk.path} · ${disk.model || "Unknown"} · ${disk.sizeHuman}`;
  els.repairTarget.textContent = `${disk.path} · ${disk.model || "Unknown"} · ${disk.rotational ? "HDD" : "SSD/NVMe"}`;
  els.startBtn.disabled = false;
  els.repairConfirm.placeholder = `填写 ${disk.name}`;
  els.repairConfirm.value = "";
  const volumeMount = pickVolumeMountpoint(disk.mountpoint);
  if (volumeMount) {
    els.repairMountpoint.value = volumeMount;
  }
  renderDisks();
  syncRepairButton();
  loadSmart(disk.name);
}

function mountSummary(mountpoint) {
  const list = String(mountpoint || "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  if (!list.length) {
    return "未挂载";
  }
  const shown = list.slice(0, 2).join(", ");
  return list.length > 2 ? `已挂载 ${shown} 等 ${list.length} 处` : `已挂载 ${shown}`;
}

function pickVolumeMountpoint(mountpoint) {
  const list = String(mountpoint || "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  if (!list.length) return "";
  return list.find((item) => item.startsWith("/vol")) || list[0];
}

function syncRepairButton() {
  const mode = els.repairMode.value;
  let ready = Boolean(state.selected) && !state.repairEvents;
  if (mode === "scrub") {
    ready = ready && Boolean(els.repairMountpoint.value.trim());
  } else {
    ready = ready && Boolean(state.scan) && state.scan.status !== "running" && (state.scan.badBlockList || []).length > 0;
  }
  els.repairBtn.disabled = !ready;
}

async function startScan() {
  if (!state.selected) return;
  if (state.events) state.events.close();
  els.log.textContent = "";
  setProgress(0, 0);
  els.scanStatus.textContent = "启动中";
  els.startBtn.disabled = true;
  els.stopBtn.disabled = false;
  try {
    const scan = await fetchJSON("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        path: state.selected.path,
        blockSize: Number(els.blockSize.value),
      }),
    });
    state.scan = scan;
    connectEvents(scan.id);
  } catch (err) {
    els.scanStatus.textContent = "启动失败";
    appendLog(err.message);
    els.startBtn.disabled = false;
    els.stopBtn.disabled = true;
  }
}

function connectEvents(id) {
  const source = new EventSource(apiPath(`/api/scans/${id}/events`));
  state.events = source;
  source.addEventListener("line", (event) => appendLog(event.data));
  source.addEventListener("state", (event) => {
    const scan = JSON.parse(event.data);
    state.scan = scan;
    els.scanStatus.textContent = statusLabel(scan.status);
    setProgress(scan.progress, (scan.badBlocks || 0) + (scan.errRead || 0) + (scan.errWrite || 0) + (scan.errCompare || 0));
    setStats(scan);
    if (scan.status !== "running") {
      els.startBtn.disabled = !state.selected;
      els.stopBtn.disabled = true;
      source.close();
      state.events = null;
      syncRepairButton();
    }
  });
  source.onerror = () => {
    appendLog("实时连接中断，正在等待服务端状态。");
  };
}

async function stopScan() {
  if (!state.scan) return;
  els.stopBtn.disabled = true;
  await fetchJSON(`/api/scans/${state.scan.id}/stop`, { method: "POST" }).catch((err) => appendLog(err.message));
}

async function loadCapabilities() {
  try {
    state.capabilities = await fetchJSON("/api/repair/capabilities");
    applyCapabilities();
  } catch (err) {
    els.advice.textContent = `修复能力探测失败：${err.message}`;
  }
}

function applyCapabilities() {
  const caps = state.capabilities;
  if (!caps) return;
  const missing = [];
  if (!caps.badblocks) missing.push("badblocks");
  if (!caps.smartctl) missing.push("smartctl");
  if (!caps.btrfs) missing.push("btrfs");
  els.advice.textContent = missing.length
    ? `${caps.notice || ""} 当前系统缺少：${missing.join("、")}`
    : `${caps.notice || ""}`;
  [...els.repairMode.options].forEach((opt) => {
    if (opt.value === "scrub") opt.disabled = !caps.btrfs;
    if (opt.value === "remap") opt.disabled = !caps.badblocks;
  });
}

async function loadSmart(name) {
  els.smartTable.innerHTML = "<div class=\"muted\">读取 SMART…</div>";
  try {
    const data = await fetchJSON(`/api/smart/${encodeURIComponent(name)}`);
    renderSmart(data.smart || {});
  } catch (err) {
    els.smartTable.innerHTML = `<div class="muted">SMART 读取失败：${escapeHTML(err.message)}</div>`;
  }
}

function renderSmart(smart) {
  if (!smart.available) {
    els.smartTable.innerHTML = `<div class="muted">${escapeHTML(smart.message || "SMART 不可用")}</div>`;
    return;
  }
  const rows = [
    ["健康状态", smart.health || "UNKNOWN"],
    ["已重映射扇区", smart.reallocated ?? 0],
    ["待定扇区", smart.pending ?? 0],
    ["离线不可纠正", smart.offlineUncorrectable ?? 0],
    ["报告不可纠正错误", smart.reportedUncorrect ?? 0],
    ["通电小时", smart.powerOnHours ?? 0],
  ];
  els.smartTable.innerHTML = rows
    .map(([label, value]) => `<div class="smart-row"><span>${label}</span><strong>${escapeHTML(value)}</strong></div>`)
    .join("");
}

async function startRepair() {
  if (!state.selected || state.repairEvents) return;
  const mode = els.repairMode.value;
  els.repairLog.textContent = "";
  els.repairStatus.textContent = "启动中";
  els.repairBtn.disabled = true;
  els.repairStopBtn.disabled = false;
  const payload = {
    path: state.selected.path,
    mode,
    blockSize: Number(els.blockSize.value),
    margin: Number(els.repairMargin.value || 0),
    dryRun: els.repairDryRun.checked,
    confirm: els.repairConfirm.value.trim(),
    mountpoint: els.repairMountpoint.value.trim(),
  };
  if (mode !== "scrub" && state.scan) {
    payload.scanId = state.scan.id;
    payload.badBlocks = state.scan.badBlockList || [];
  }
  try {
    const rep = await fetchJSON("/api/repair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    state.repair = rep;
    renderRepair(rep);
    connectRepairEvents(rep.id);
  } catch (err) {
    els.repairStatus.textContent = "启动失败";
    appendRepairLog(err.message);
    els.repairBtn.disabled = false;
    els.repairStopBtn.disabled = true;
  }
}

function connectRepairEvents(id) {
  const source = new EventSource(apiPath(`/api/repairs/${id}/events`));
  state.repairEvents = source;
  source.addEventListener("line", (event) => appendRepairLog(event.data));
  source.addEventListener("state", (event) => {
    const rep = JSON.parse(event.data);
    state.repair = rep;
    renderRepair(rep);
    if (rep.status !== "running") {
      source.close();
      state.repairEvents = null;
      els.repairStopBtn.disabled = true;
      syncRepairButton();
      if (state.selected) loadSmart(state.selected.name);
    }
  });
  source.onerror = () => appendRepairLog("修复实时连接中断。");
}

function renderRepair(rep) {
  els.repairStatus.textContent = statusLabel(rep.status);
  els.repairBadBlocks.textContent = String(rep.badBlocks || 0);
  els.repairRanges.textContent = String((rep.ranges || []).length);
  els.repairProgress.textContent = `${(Number(rep.progress) || 0).toFixed(1)}%`;
  els.repairResult.textContent = `${rep.success || 0} / ${rep.failed || 0}`;
  els.repairImprovement.textContent = typeof rep.improvement === "number" ? String(rep.improvement) : "-";
  if (rep.advice) els.advice.textContent = rep.advice;
  if (rep.smartAfter && rep.smartAfter.available) renderSmart(rep.smartAfter);
  if (rep.error) appendRepairLog(`错误：${rep.error}`);
}

async function stopRepair() {
  if (!state.repair) return;
  els.repairStopBtn.disabled = true;
  await fetchJSON(`/api/repairs/${state.repair.id}/stop`, { method: "POST" }).catch((err) => appendRepairLog(err.message));
}

function statusLabel(status) {
  return {
    running: "运行中",
    finished: "完成",
    failed: "失败",
    stopped: "已停止",
  }[status] || "待机";
}

function appendLog(line) {
  els.log.textContent += `${line}\n`;
  els.log.scrollTop = els.log.scrollHeight;
}

function appendRepairLog(line) {
  els.repairLog.textContent += `${line}\n`;
  els.repairLog.scrollTop = els.repairLog.scrollHeight;
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "\"": "&quot;",
    "'": "&#039;",
  }[ch]));
}

els.refreshBtn.addEventListener("click", loadDisks);
els.startBtn.addEventListener("click", startScan);
els.stopBtn.addEventListener("click", stopScan);
els.repairBtn.addEventListener("click", startRepair);
els.repairStopBtn.addEventListener("click", stopRepair);
els.repairMode.addEventListener("change", syncRepairButton);
els.repairMountpoint.addEventListener("input", syncRepairButton);

// 飞牛桌面以 iframe 内嵌本页；提供独立窗口入口
if (window.top !== window.self) {
  els.popoutBtn.hidden = false;
  els.popoutBtn.addEventListener("click", () => {
    window.open(location.href, "_blank", "width=1280,height=900");
  });
}

createSurface();
loadDisks();
loadCapabilities();
