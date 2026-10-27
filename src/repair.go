package main

// 坏道修复（Remediation）模块
//
// 设计原则（务必遵守）：
//  1. 物理坏道不可被"修复"。本模块做的是三件事：
//     a) remap  —— 对坏块区间做非破坏性写测试（badblocks -n），促使硬盘固件把不稳定扇区重映射到备用扇区；
//     b) scrub  —— 对 btrfs 卷执行 scrub，用校验和发现坏数据，有冗余时自动修复；
//     c) recheck—— 修复后对坏块区间只读复扫，验证是否真的消失。
//  2. 所有写入类动作必须显式 confirm 匹配盘符，默认 dryRun。
//  3. remap 仅允许用于未挂载、未参与在线阵列的机械盘（HDD）；SSD/NVMe 与在用盘一律拒绝。
//  4. 扫描/修复全程不隐藏风险：SMART 前后对比写入结果，未改善的坏块如实报告并给出换盘建议。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 数据结构 ----------

// SmartStats 是修复前后用于对比的 SMART 关键属性。
type SmartStats struct {
	Available            bool   `json:"available"`
	Health               string `json:"health,omitempty"`
	Reallocated          int    `json:"reallocated"`
	Pending              int    `json:"pending"`
	OfflineUncorrectable int    `json:"offlineUncorrectable"`
	ReportedUncorrect    int    `json:"reportedUncorrect"`
	PowerOnHours         int    `json:"powerOnHours"`
	Message              string `json:"message,omitempty"`
}

// Range 是连续的坏块区间（单位：badblocks 块号）。
type Range struct {
	First int64 `json:"first"`
	Last  int64 `json:"last"`
	Count int64 `json:"count"`
}

type repairRequest struct {
	ScanID    string  `json:"scanId"`
	Path      string  `json:"path"`
	Mode      string  `json:"mode"`
	BlockSize int     `json:"blockSize"`
	Confirm   string  `json:"confirm"`
	DryRun    bool    `json:"dryRun"`
	Margin    int64   `json:"margin"`
	Mountpoint string `json:"mountpoint"`
	BadBlocks []int64 `json:"badBlocks"`
}

type RepairStep struct {
	Index    int    `json:"index"`
	Command  string `json:"command"`
	Range    Range  `json:"range"`
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output,omitempty"`
}

type Repair struct {
	ID         string      `json:"id"`
	Path       string      `json:"path"`
	Mode       string      `json:"mode"`
	Status     string      `json:"status"`
	DryRun     bool        `json:"dryRun"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt time.Time   `json:"finishedAt,omitempty"`
	BlockSize  int         `json:"blockSize"`
	Total      int         `json:"total"`
	Processed  int         `json:"processed"`
	Success    int         `json:"success"`
	Failed     int         `json:"failed"`
	Progress   float64     `json:"progress"`
	Elapsed    string      `json:"elapsed"`
	BadBlocks  int         `json:"badBlocks"`
	Ranges     []Range     `json:"ranges"`
	Steps      []RepairStep `json:"steps"`
	SmartBefore SmartStats `json:"smartBefore"`
	SmartAfter  SmartStats `json:"smartAfter"`
	Improvement int        `json:"improvement"`
	Advice      string     `json:"advice,omitempty"`
	Error       string     `json:"error,omitempty"`

	ctx    context.Context
	cancel context.CancelFunc
	lines  []string
	subs   map[chan string]struct{}
	mu     sync.Mutex
}

// ---------- 能力探测 ----------

func (s *Server) handleRepairCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"badblocks": commandExists("badblocks"),
		"hdparm":    commandExists("hdparm"),
		"smartctl":  commandExists("smartctl"),
		"btrfs":     commandExists("btrfs"),
		"e2fsck":    commandExists("e2fsck"),
		"modes": map[string]string{
			"remap":   "坏块区间非破坏性重写（badblocks -n），促使硬盘固件重映射备用扇区；要求未挂载、非阵列成员、机械盘",
			"scrub":   "btrfs scrub 校验并利用冗余修复坏数据；必须在已挂载路径上执行",
			"recheck": "修复后坏块区间只读复扫，验证坏道是否消失",
		},
		"notice": "物理坏道无法真正修复，只能重映射或隔离；写入类操作前请务必备份重要数据。",
	})
}

// ---------- SMART ----------

var smartAttrRE = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+0x[0-9a-fA-F]+\s+(\d+)\s+(\d+)\s+(\d+)\s+\S+\s+\S+\s+(\S+)`)

func readSmart(path string) SmartStats {
	out := SmartStats{}
	if !commandExists("smartctl") {
		out.Message = "smartctl 不可用"
		return out
	}
	b, err := exec.Command("smartctl", "-H", "-A", "-n", "standby", path).CombinedOutput()
	text := string(b)
	if err != nil && strings.TrimSpace(text) == "" {
		out.Message = "SMART 读取失败"
		return out
	}
	out.Available = true
	for _, line := range strings.Split(text, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "overall-health") {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				out.Health = strings.TrimSpace(parts[1])
			}
		}
		m := smartAttrRE.FindStringSubmatch(line)
		if len(m) != 7 {
			continue
		}
		raw := m[6]
		num := parseLeadingInt(raw)
		switch m[2] {
		case "Reallocated_Sector_Ct", "Reallocated_Event_Count":
			out.Reallocated = num
		case "Current_Pending_Sector", "Current_Pending_ECC_Cnt":
			out.Pending = num
		case "Offline_Uncorrectable", "Offline_Uncorrectable_Ct":
			out.OfflineUncorrectable = num
		case "Reported_Uncorrect", "Reported_Uncorrectable_Errors":
			out.ReportedUncorrect = num
		case "Power_On_Hours":
			out.PowerOnHours = num
		}
	}
	if out.Health == "" {
		out.Health = "UNKNOWN"
	}
	return out
}

func parseLeadingInt(s string) int {
	s = strings.TrimSpace(s)
	digits := make([]rune, 0, 8)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
			continue
		}
		break
	}
	n, _ := strconv.Atoi(string(digits))
	return n
}

func (s *Server) handleSmart(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/smart/")
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid disk"})
		return
	}
	path := name
	if !strings.HasPrefix(path, "/dev/") {
		path = "/dev/" + filepath.Base(name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "smart": readSmart(path)})
}

// ---------- 区间计算 ----------

// mergeRanges 把坏块列表合并为连续区间，并按 margin 向外扩展，便于触发整段重映射。
func mergeRanges(blocks []int64, margin int64) []Range {
	if len(blocks) == 0 {
		return nil
	}
	sorted := append([]int64(nil), blocks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var ranges []Range
	cur := Range{First: sorted[0], Last: sorted[0], Count: 1}
	for _, b := range sorted[1:] {
		if b == cur.Last || b == cur.Last+1 {
			cur.Last = b
			cur.Count++
			continue
		}
		ranges = append(ranges, cur)
		cur = Range{First: b, Last: b, Count: 1}
	}
	ranges = append(ranges, cur)

	for i := range ranges {
		if margin > 0 {
			ranges[i].First -= margin
			if ranges[i].First < 0 {
				ranges[i].First = 0
			}
			ranges[i].Last += margin
			ranges[i].Count = ranges[i].Last - ranges[i].First + 1
		}
	}
	return ranges
}

// ---------- 安全前置检查 ----------

type diskUsage struct {
	Mounted []string
	Holders []string
}

type lsblkNode struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Mountpoints []string `json:"mountpoints"`
	Mountpoint  string   `json:"mountpoint"`
}

func inspectDiskUsage(path string) (diskUsage, error) {
	var u diskUsage
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,TYPE,MOUNTPOINTS,MOUNTPOINT", path).Output()
	if err != nil {
		return u, err
	}
	var parsed struct {
		BlockDevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return u, err
	}
	var walk func(devs []lsblkNode)
	walk = func(devs []lsblkNode) {
		for _, d := range devs {
			for _, mp := range compact(d.Mountpoints) {
				if mp != "" {
					u.Mounted = append(u.Mounted, mp)
				}
			}
			if strings.TrimSpace(d.Mountpoint) != "" {
				u.Mounted = append(u.Mounted, strings.TrimSpace(d.Mountpoint))
			}
			if d.Type == "raid1" || d.Type == "raid0" || d.Type == "raid5" || d.Type == "raid6" ||
				d.Type == "raid10" || d.Type == "lvm" || d.Type == "crypt" || d.Type == "zfs_member" {
				u.Holders = append(u.Holders, d.Name+"("+d.Type+")")
			}
		}
	}
	walk(parsed.BlockDevices)
	u.Mounted = compact(u.Mounted)
	// zfs 成员盘的挂载点不在 lsblk 树里，需要借助 zpool 反查。
	if len(u.Mounted) == 0 {
		if extra := zfsVolumeMounts()[diskBaseName(path)]; len(extra) > 0 {
			u.Mounted = compact(extra)
		}
	}
	return u, nil
}

// ---------- 启动修复 ----------

func (s *Server) handleStartRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req repairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	req.Mode = strings.TrimSpace(req.Mode)
	if req.Mode == "" {
		req.Mode = "remap"
	}
	if req.Mode != "remap" && req.Mode != "scrub" && req.Mode != "recheck" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode must be remap / scrub / recheck"})
		return
	}
	if err := s.validateDisk(req.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.BlockSize == 0 {
		req.BlockSize = 4096
	}
	if req.BlockSize < 512 || req.BlockSize > 1048576 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "blockSize must be between 512 and 1048576"})
		return
	}
	if req.Margin < 0 {
		req.Margin = 0
	}
	if req.Margin > 4096 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "margin too large (max 4096 blocks)"})
		return
	}

	// 坏块来源：优先取扫描结果，其次取请求体
	blocks := req.BadBlocks
	if req.ScanID != "" {
		scan := s.getScan(req.ScanID)
		if scan == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "scan not found"})
			return
		}
		if scan.Status == "running" {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "扫描仍在进行，请先停止或等待完成"})
			return
		}
		if scan.Path != req.Path {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "scan path mismatch"})
			return
		}
		scan.mu.Lock()
		blocks = append([]int64(nil), scan.BadBlockList...)
		if req.BlockSize == 4096 && scan.BlockSize > 0 {
			req.BlockSize = scan.BlockSize
		}
		scan.mu.Unlock()
	}

	dryRun := req.DryRun
	name := filepath.Base(req.Path)

	// 写入类操作必须显式确认盘符
	if !dryRun && req.Mode != "recheck" {
		if strings.TrimSpace(req.Confirm) == "" || strings.TrimSpace(req.Confirm) != name {
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{
				"error":   "确认失败：请在 confirm 字段填写磁盘名 " + name + " 才能执行写入类修复",
				"confirm": name,
			})
			return
		}
	}

	// remap 的安全前置条件
	if req.Mode == "remap" && !dryRun {
		disk, ok := s.lookupDisk(req.Path)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "disk not found"})
			return
		}
		if !disk.Rotational {
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{
				"error": "该盘为 SSD/NVMe，不支持 remap 重映射。请改用 scrub（btrfs）或直接更换硬件。",
			})
			return
		}
		usage, err := inspectDiskUsage(req.Path)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "lsblk failed: " + err.Error()})
			return
		}
		if len(usage.Mounted) > 0 {
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{
				"error":    "该盘存在挂载点，remap 需要非破坏性写测试，必须先在 fnOS 中卸载卷",
				"mounted":  usage.Mounted,
			})
			return
		}
		if len(usage.Holders) > 0 {
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{
				"error":   "该盘是 RAID/LVM 阵列成员，在线状态下不允许写测试，请先在存储管理中移除/停用该盘",
				"holders": usage.Holders,
			})
			return
		}
	}

	if req.Mode == "scrub" {
		if !commandExists("btrfs") {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "btrfs 命令不可用"})
			return
		}
		if strings.TrimSpace(req.Mountpoint) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "scrub 需要提供已挂载的 btrfs 路径 mountpoint"})
			return
		}
	}

	if (req.Mode == "remap" || req.Mode == "recheck") && len(blocks) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有坏块数据：请先完成一次坏道扫描"})
		return
	}
	if req.Mode == "remap" && !commandExists("badblocks") {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "badblocks 命令不可用"})
		return
	}

	ranges := mergeRanges(blocks, req.Margin)
	id := randomID()
	ctx, cancel := context.WithCancel(context.Background())
	rep := &Repair{
		ID:        id,
		Path:      req.Path,
		Mode:      req.Mode,
		Status:    "running",
		DryRun:    dryRun,
		StartedAt: time.Now(),
		BlockSize: req.BlockSize,
		BadBlocks: len(blocks),
		Ranges:    ranges,
		Total:     len(ranges),
		ctx:       ctx,
		cancel:    cancel,
		subs:      make(map[chan string]struct{}),
	}
	if req.Mode == "scrub" {
		rep.Total = 1
	}

	s.mu.Lock()
	s.repairs[id] = rep
	s.mu.Unlock()

	go s.runRepair(rep, req)
	writeJSON(w, http.StatusAccepted, rep.snapshot())
}

func (s *Server) lookupDisk(path string) (Disk, bool) {
	disks, err := listDisks()
	if err != nil {
		return Disk{}, false
	}
	for _, d := range disks {
		if d.Path == path {
			return d, true
		}
	}
	return Disk{}, false
}

// ---------- 执行修复 ----------

func (s *Server) runRepair(rep *Repair, req repairRequest) {
	rep.addLine(fmt.Sprintf("== 修复任务开始 mode=%s path=%s dryRun=%v ==", rep.Mode, rep.Path, rep.DryRun))

	rep.addLine("> 采集修复前 SMART 基线")
	before := readSmart(rep.Path)
	rep.mu.Lock()
	rep.SmartBefore = before
	rep.mu.Unlock()
	if before.Available {
		rep.addLine(fmt.Sprintf("  重映射=%d 待定扇区=%d 离线不可纠正=%d 报告错误=%d 健康=%s",
			before.Reallocated, before.Pending, before.OfflineUncorrectable, before.ReportedUncorrect, before.Health))
	} else {
		rep.addLine("  SMART 不可用：" + before.Message)
	}

	if rep.Mode == "scrub" {
		s.runScrub(rep, req)
	} else {
		s.runBadblocksRanges(rep, req)
	}

	if rep.ctx.Err() == context.Canceled {
		rep.finish("stopped", errors.New("修复被用户停止"))
		return
	}

	rep.addLine("> 采集修复后 SMART 数据")
	after := readSmart(rep.Path)
	rep.mu.Lock()
	rep.SmartAfter = after
	improvement := 0
	if after.Available && before.Available {
		improvement = (before.Pending + before.OfflineUncorrectable) - (after.Pending + after.OfflineUncorrectable)
	}
	rep.Improvement = improvement
	rep.Advice = buildAdvice(before, after, rep)
	rep.mu.Unlock()
	if after.Available {
		rep.addLine(fmt.Sprintf("  重映射=%d 待定扇区=%d 离线不可纠正=%d 报告错误=%d 健康=%s",
			after.Reallocated, after.Pending, after.OfflineUncorrectable, after.ReportedUncorrect, after.Health))
	}

	if rep.Failed > 0 && rep.Success == 0 {
		rep.finish("failed", errors.New("全部区间修复失败，硬件可能已进入不可恢复状态"))
		return
	}
	rep.finish("finished", nil)
}

func (s *Server) runBadblocksRanges(rep *Repair, req repairRequest) {
	for i, rg := range rep.Ranges {
		if rep.ctx.Err() != nil {
			return
		}
		args := []string{"-b", strconv.FormatInt(int64(rep.BlockSize), 10)}
		if rep.Mode == "remap" {
			args = append([]string{"-n", "-p", "1"}, args...)
		} else {
			args = append([]string{"-sv"}, args...)
		}
		args = append(args, rep.Path, strconv.FormatInt(rg.Last, 10), strconv.FormatInt(rg.First, 10))
		cmdline := "badblocks " + strings.Join(args, " ")

		step := RepairStep{Index: i + 1, Command: cmdline, Range: rg, ExitCode: -1}
		if rep.DryRun {
			step.Status = "dry-run"
			rep.addLine(fmt.Sprintf("[%d/%d] DRY-RUN 将执行: %s", i+1, rep.Total, cmdline))
			step.Output = "dry-run：未执行"
			rep.pushStep(step)
			rep.bumpProgress(i + 1)
			continue
		}

		rep.addLine(fmt.Sprintf("[%d/%d] 执行: %s", i+1, rep.Total, cmdline))
		code, out := runCommand(rep.ctx, "badblocks", args...)
		step.ExitCode = code
		step.Output = tail(out, 800)
		if rep.ctx.Err() != nil {
			step.Status = "stopped"
			rep.pushStep(step)
			return
		}
		if code == 0 {
			step.Status = "ok"
		} else {
			step.Status = "failed"
		}
		rep.pushStep(step)
		rep.bumpProgress(i + 1)
	}
}

func (s *Server) runScrub(rep *Repair, req repairRequest) {
	args := []string{"scrub", "start", "-B", "-r", req.Mountpoint}
	cmdline := "btrfs " + strings.Join(args, " ")
	step := RepairStep{Index: 1, Command: cmdline, ExitCode: -1}
	if rep.DryRun {
		step.Status = "dry-run"
		step.Output = "dry-run：未执行"
		rep.addLine("DRY-RUN 将执行: " + cmdline)
		rep.pushStep(step)
		rep.bumpProgress(1)
		return
	}
	rep.addLine("执行: " + cmdline)
	code, out := runCommand(rep.ctx, "btrfs", args...)
	step.ExitCode = code
	step.Output = tail(out, 2000)
	if code == 0 {
		step.Status = "ok"
	} else {
		step.Status = "failed"
	}
	rep.pushStep(step)
	rep.bumpProgress(1)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) != "" {
			rep.addLine("  " + line)
		}
	}
}

func runCommand(ctx context.Context, name string, args ...string) (int, string) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
			out = append(out, []byte("\n"+err.Error())...)
		}
	}
	return code, string(out)
}

func buildAdvice(before, after SmartStats, rep *Repair) string {
	var sb strings.Builder
	if rep.DryRun {
		sb.WriteString("本次为演练模式，未对磁盘写入任何数据。")
	}
	if rep.Failed > 0 {
		sb.WriteString(fmt.Sprintf("有 %d 个区间处理失败，说明坏道已无法通过重映射隔离；请立即备份数据并将该盘列入更换计划。", rep.Failed))
	}
	if after.Available && before.Available {
		if after.Reallocated > before.Reallocated {
			sb.WriteString(fmt.Sprintf("重映射扇区从 %d 增至 %d，固件已接管坏道，属预期结果；请继续观察待定扇区是否归零。", before.Reallocated, after.Reallocated))
		}
		if after.Pending > 0 {
			sb.WriteString(fmt.Sprintf("仍有 %d 个待定扇区未消除，建议再跑一次 remap 或安排换盘。", after.Pending))
		}
		if after.Pending == 0 && before.Pending > 0 {
			sb.WriteString("待定扇区已归零，坏道已隔离完成。")
		}
	}
	sb.WriteString("物理坏道不能真正修复，重映射只是用备用扇区顶替；坏道数量持续增长时唯一可靠做法是更换硬盘。")
	return strings.TrimSpace(sb.String())
}

// ---------- Repair 并发安全与流式输出 ----------

func (rep *Repair) addLine(line string) {
	line = strings.TrimSpace(cleanTerminalLine(line))
	if line == "" {
		return
	}
	rep.mu.Lock()
	rep.lines = append(rep.lines, time.Now().Format("15:04:05")+"  "+line)
	if len(rep.lines) > 2000 {
		rep.lines = rep.lines[len(rep.lines)-2000:]
	}
	msg := rep.lines[len(rep.lines)-1]
	for ch := range rep.subs {
		select {
		case ch <- msg:
		default:
		}
	}
	rep.mu.Unlock()
}

func (rep *Repair) pushStep(step RepairStep) {
	rep.mu.Lock()
	rep.Steps = append(rep.Steps, step)
	if step.Status == "ok" || step.Status == "dry-run" {
		rep.Success++
	} else {
		rep.Failed++
	}
	rep.mu.Unlock()
}

func (rep *Repair) bumpProgress(done int) {
	rep.mu.Lock()
	rep.Processed = done
	if rep.Total > 0 {
		rep.Progress = float64(done) / float64(rep.Total) * 100
		if rep.Progress > 100 {
			rep.Progress = 100
		}
	}
	rep.mu.Unlock()
}

func (rep *Repair) finish(status string, err error) {
	rep.mu.Lock()
	rep.Status = status
	rep.FinishedAt = time.Now()
	if err != nil {
		rep.Error = err.Error()
	}
	if status == "finished" {
		rep.Progress = 100
	}
	rep.mu.Unlock()
	rep.addLine(fmt.Sprintf("== 修复任务 %s，成功 %d / 失败 %d ==", status, rep.Success, rep.Failed))
}

func (rep *Repair) subscribe() chan string {
	ch := make(chan string, 64)
	rep.mu.Lock()
	rep.subs[ch] = struct{}{}
	rep.mu.Unlock()
	return ch
}

func (rep *Repair) unsubscribe(ch chan string) {
	rep.mu.Lock()
	delete(rep.subs, ch)
	close(ch)
	rep.mu.Unlock()
}

func (rep *Repair) recentLines() []string {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	return append([]string(nil), rep.lines...)
}

func (rep *Repair) snapshot() Repair {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	cp := *rep
	formatDuration := func(d time.Duration) string {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		s := int(d.Seconds()) % 60
		if h > 0 {
			return fmt.Sprintf("%d:%02d:%02d", h, m, s)
		}
		return fmt.Sprintf("%d:%02d", m, s)
	}
	if cp.Status == "running" {
		cp.Elapsed = formatDuration(time.Since(cp.StartedAt).Round(time.Second))
	} else if !cp.FinishedAt.IsZero() {
		cp.Elapsed = formatDuration(cp.FinishedAt.Sub(cp.StartedAt).Round(time.Second))
	}
	cp.ctx = nil
	cp.cancel = nil
	cp.subs = nil
	cp.lines = nil
	return cp
}

func (s *Server) getRepair(id string) *Repair {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repairs[id]
}

func (s *Server) handleRepairByID(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/repairs/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rep := s.getRepair(parts[0])
	if rep == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "repair not found"})
		return
	}
	if len(parts) == 2 && parts[1] == "events" {
		s.handleRepairEvents(w, r, rep)
		return
	}
	if len(parts) == 2 && parts[1] == "stop" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rep.cancel()
		rep.addLine("收到停止请求")
		writeJSON(w, http.StatusOK, rep.snapshot())
		return
	}
	writeJSON(w, http.StatusOK, rep.snapshot())
}

func (s *Server) handleRepairEvents(w http.ResponseWriter, r *http.Request, rep *Repair) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := rep.subscribe()
	defer rep.unsubscribe(ch)

	for _, line := range rep.recentLines() {
		writeEvent(w, "line", line)
	}
	writeEventJSON(w, "state", rep.snapshot())
	flusher.Flush()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			writeEvent(w, "line", line)
			writeEventJSON(w, "state", rep.snapshot())
			flusher.Flush()
			if rep.isFinished() {
				return
			}
		case <-ticker.C:
			writeEventJSON(w, "state", rep.snapshot())
			flusher.Flush()
			if rep.isFinished() {
				return
			}
		}
	}
}

func (rep *Repair) isFinished() bool {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	return rep.Status != "running"
}

func tail(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}
