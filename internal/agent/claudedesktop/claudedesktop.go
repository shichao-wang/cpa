// Package claudedesktop 管理 macOS Claude Desktop 的本地第三方推理配置。
package claudedesktop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/store"
)

const journalVersion = 1
const metaFile = "Claude-3p/configLibrary/_meta.json"
const primaryModeFile = "Claude/claude_desktop_config.json"
const thirdPartyModeFile = "Claude-3p/claude_desktop_config.json"
const entryField = "@cpaEntry"

var configFields = []string{"inferenceProvider", "inferenceCredentialKind", "inferenceGatewayBaseUrl", "inferenceGatewayApiKey", "inferenceGatewayAuthScheme", "inferenceModels", "modelDiscoveryEnabled", "chatTabEnabled", "isClaudeCodeForDesktopEnabled", "coworkTabEnabled"}

// transactionHook 仅由包内故障测试注入；每次操作独立，不共享可变钩子。
type transactionHook func(stage, name string) error

type value struct {
	Present bool `json:"present"`
	Data    any  `json:"data,omitempty"`
}
type snapshot struct {
	Exists bool           `json:"exists"`
	Mode   uint32         `json:"mode,omitempty"`
	Data   map[string]any `json:"data"`
}
type change struct {
	Original value `json:"original"`
	Applied  value `json:"applied"`
}
type fileRecord struct {
	Original snapshot          `json:"original"`
	Fields   map[string]change `json:"fields"`
	Prior    map[string]value  `json:"prior,omitempty"`
	Goal     map[string]value  `json:"goal,omitempty"`
}
type journal struct {
	Version        int                    `json:"version"`
	Root           string                 `json:"root"`
	ApplyID        string                 `json:"applyID"`
	EntryID        string                 `json:"entryID"`
	ProfileID      string                 `json:"profileID"`
	ProfileName    string                 `json:"profileName"`
	PriorProfileID string                 `json:"priorProfileID,omitempty"`
	Status         string                 `json:"status"`
	Action         string                 `json:"action"`
	Files          map[string]*fileRecord `json:"files"`
}

// ResolveConfigDir 返回包含 Claude 和 Claude-3p 两个目录的 Application Support 根目录。
// 空值仅在 macOS 使用 ~/Library/Application Support；显式路径便于临时目录测试。
func ResolveConfigDir(dir string) (string, error) {
	if dir == "" {
		if runtime.GOOS != "darwin" {
			return "", fmt.Errorf("Claude Desktop 仅支持 macOS，请在 macOS 配置")
		}
		current, err := user.Current()
		if err != nil || current.HomeDir == "" {
			return "", fmt.Errorf("无法确认 macOS 用户的 Desktop 配置目录，请显式指定 --config-dir")
		}
		dir = filepath.Join(current.HomeDir, "Library", "Application Support")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析 Desktop 配置目录失败：%w", err)
	}
	abs = normalizeSystemAliases(filepath.Clean(abs))
	if err := rejectSymlinkAncestors(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// macOS 的系统别名先转成固定物理前缀；其他祖先符号链接一律拒绝。
func normalizeSystemAliases(path string) string {
	if runtime.GOOS != "darwin" {
		return path
	}
	for _, prefix := range []string{"/tmp", "/var", "/etc"} {
		if path != prefix && !strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			continue
		}
		target, err := filepath.EvalSymlinks(prefix)
		if err == nil && target == "/private"+prefix {
			return target + strings.TrimPrefix(path, prefix)
		}
	}
	return path
}
func rejectSymlinkAncestors(path string) error {
	for current := normalizeSystemAliases(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("Desktop 配置路径祖先必须是实际目录，拒绝符号链接：%s", current)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

// Apply 从最新状态派生四档模型，只保存下游 profile key。configDir 是 Application Support 根目录。
func Apply(s *store.Store, profile config.Profile, configDir string) error {
	return apply(s, profile, configDir, nil)
}
func apply(s *store.Store, profile config.Profile, configDir string, hook transactionHook) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	root, err := ResolveConfigDir(configDir)
	if err != nil {
		return err
	}
	return store.WithLock(s.Path("state.lock"), func() error {
		return withLock(s, func() error {
			state, err := s.Read()
			if err != nil {
				return err
			}
			stored, ok := state.Profiles[profile.Name]
			if !ok || stored.ID != profile.ID {
				return fmt.Errorf("profile 已不存在或 ID 已变化，拒绝应用")
			}
			profile = stored
			if profile.Agent != "claude-desktop" {
				return fmt.Errorf("profile 的 agent 不是 claude-desktop")
			}
			if err := config.ValidateProfileModels(profile.Models); err != nil {
				return err
			}
			secret := state.Secrets[profile.KeyRef]
			if profile.KeyRef == "" || strings.TrimSpace(secret) == "" {
				return fmt.Errorf("profile 缺少有效的 API key")
			}
			gateway, err := gatewayURL(state.Listen)
			if err != nil {
				return err
			}
			if err := checkEnvironment(root, isDefaultRoot(root, configDir)); err != nil {
				return err
			}
			j, err := readJournal(s)
			if err != nil {
				return err
			}
			if j != nil {
				if err := validateJournal(j); err != nil {
					return err
				}
				if j.Root != root {
					return fmt.Errorf("cpa 已管理另一 Desktop 配置目录，拒绝切换")
				}
				if j.Status == "pending" {
					if err := resume(s, j, hook); err != nil {
						return err
					}
					j, err = readJournal(s)
					if err != nil {
						return err
					}
				}
			}
			var current map[string]snapshot
			if j == nil {
				id, err := config.RandomID()
				if err != nil {
					return err
				}
				id = uuid(id)
				id = id[:14] + "4" + id[15:19] + "8" + id[20:]
				applyID, err := config.RandomID()
				if err != nil {
					return err
				}
				j = &journal{Version: journalVersion, Root: root, ApplyID: applyID, EntryID: id, Files: map[string]*fileRecord{}}
				for _, name := range fileNames(j) {
					snap, err := readFile(root, name)
					if err != nil {
						return err
					}
					if name == configFile(j) && snap.Exists {
						return fmt.Errorf("生成的 Desktop 配置项已存在，拒绝覆盖")
					}
					j.Files[name] = &fileRecord{Original: snap, Fields: map[string]change{}}
				}
			} else if j.Action != "apply" || j.Status != "committed" {
				return fmt.Errorf("没有有效的 Desktop 应用基线")
			}
			current, err = readAll(j)
			if err != nil {
				return err
			}
			if j.ProfileID != "" {
				if err := checkApplied(j, current); err != nil {
					return err
				}
			}
			desired := desiredFields(state, profile, gateway, secret, j.EntryID)
			for name, fields := range desired {
				rec := j.Files[name]
				rec.Prior = map[string]value{}
				rec.Goal = map[string]value{}
				for key, v := range fields {
					old, ok := rec.Fields[key]
					if !ok {
						old.Original, err = field(rec.Original.Data, key, j.EntryID)
						if err != nil {
							return err
						}
					}
					old.Applied = value{Present: true, Data: v}
					if key == "hybridPointer" {
						old.Applied = value{}
					}
					rec.Fields[key] = old
					rec.Prior[key], err = field(current[name].Data, key, j.EntryID)
					if err != nil {
						return err
					}
					rec.Goal[key] = old.Applied
				}
			}
			j.PriorProfileID = j.ProfileID
			j.ProfileID = profile.ID
			j.ProfileName = profile.Name
			j.Status = "pending"
			j.Action = "apply"
			if err := writeJournal(s, j); err != nil {
				return err
			}
			return resume(s, j, hook)
		})
	})
}

// Restore 先校验全部受管字段，再跨文件恢复首次接管的字段，保留其他配置与会话。
func Restore(s *store.Store, configDir string) error {
	return restore(s, configDir, nil)
}
func restore(s *store.Store, configDir string, hook transactionHook) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	root, err := ResolveConfigDir(configDir)
	if err != nil {
		return err
	}
	return withLock(s, func() error {
		if err := checkEnvironment(root, isDefaultRoot(root, configDir)); err != nil {
			return err
		}
		j, err := readJournal(s)
		if err != nil {
			return err
		}
		if j == nil {
			return fmt.Errorf("没有有效的 cpa Claude Desktop 管理记录，拒绝恢复")
		}
		if err := validateJournal(j); err != nil {
			return err
		}
		if j.Root != root {
			return fmt.Errorf("Desktop 配置目录与管理记录不匹配")
		}
		if j.Status == "pending" {
			if err := resume(s, j, hook); err != nil {
				return err
			}
			if j.Action == "restore" {
				return nil
			}
		}
		current, err := readAll(j)
		if err != nil {
			return err
		}
		if err := checkApplied(j, current); err != nil {
			return err
		}
		for _, rec := range j.Files {
			rec.Prior = map[string]value{}
			rec.Goal = map[string]value{}
			for key, ch := range rec.Fields {
				rec.Prior[key] = ch.Applied
				rec.Goal[key] = ch.Original
			}
		}
		j.Status = "pending"
		j.Action = "restore"
		if err := writeJournal(s, j); err != nil {
			return err
		}
		return resume(s, j, hook)
	})
}

// ReferencesProfileID 同时保护当前和中断切换前的 profile；管理记录读取失败返回错误。
func ReferencesProfileID(s *store.Store, profileID string) (bool, error) {
	if strings.TrimSpace(profileID) == "" {
		return false, fmt.Errorf("profile ID 不能为空")
	}
	if s == nil {
		return false, fmt.Errorf("状态存储未初始化")
	}
	found := false
	err := withLock(s, func() error {
		j, err := readJournal(s)
		if err != nil || j == nil {
			return err
		}
		if err := validateJournal(j); err != nil {
			return err
		}
		found = profileID != "" && (j.ProfileID == profileID || (j.Status == "pending" && j.PriorProfileID == profileID))
		return nil
	})
	return found, err
}

// IsManagedProfile 用于兼容已有只接受 bool 的调用方；读取失败时按受保护处理。
func IsManagedProfile(s *store.Store, name string) bool {
	if s == nil {
		return true
	}
	state, err := s.Read()
	if err != nil {
		return true
	}
	p, ok := state.Profiles[name]
	if !ok {
		return false
	}
	found, err := ReferencesProfileID(s, p.ID)
	return err != nil || found
}

func desiredFields(state *config.State, p config.Profile, gateway, secret, id string) map[string]map[string]any {
	models := []any{}
	for _, slot := range []string{"sonnet", "opus", "haiku", "fable"} {
		b := state.DisplayBinding(slot, p.Models[slot])
		models = append(models, map[string]any{"name": b.PublicModel, "labelOverride": b.Label, "anthropicFamilyTier": slot})
	}
	return map[string]map[string]any{
		primaryModeFile: {"deploymentMode": "3p"}, thirdPartyModeFile: {"deploymentMode": "3p"},
		metaFile: {"appliedId": id, entryField: map[string]any{"id": id, "name": "cpa: " + p.Name}, "hybridPointer": nil},
		"Claude-3p/configLibrary/" + id + ".json": {"inferenceProvider": "gateway", "inferenceCredentialKind": "static", "inferenceGatewayBaseUrl": gateway, "inferenceGatewayApiKey": secret, "inferenceGatewayAuthScheme": "bearer", "inferenceModels": models, "modelDiscoveryEnabled": false, "chatTabEnabled": true, "isClaudeCodeForDesktopEnabled": true, "coworkTabEnabled": false},
	}
}
func gatewayURL(listen string) (string, error) {
	raw := strings.TrimSpace(listen)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("Desktop 网关监听地址无效")
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return "", fmt.Errorf("Desktop 网关必须使用明确的 loopback 地址")
	}
	port, err := strconv.Atoi(u.Port())
	if _, _, splitErr := net.SplitHostPort(u.Host); splitErr != nil || err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("Desktop 网关监听地址必须含端口")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
func uuid(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) < 32 {
		id += strings.Repeat("0", 32-len(id))
	}
	id = id[:32]
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}
func configFile(j *journal) string { return "Claude-3p/configLibrary/" + j.EntryID + ".json" }
func fileNames(j *journal) []string {
	return []string{configFile(j), primaryModeFile, thirdPartyModeFile, metaFile}
}
func withLock(s *store.Store, fn func() error) error {
	return store.WithLock(s.Path("claudedesktop.lock"), fn)
}
func journalPath(s *store.Store) string { return s.Path("claudedesktop", "settings.json") }
func readJournal(s *store.Store) (*journal, error) {
	p := journalPath(s)
	if err := safeParents(s.Dir, "claudedesktop/settings.json", false); err != nil {
		return nil, err
	}
	if err := store.CheckFile(p); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j journal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&j); err != nil {
		return nil, fmt.Errorf("cpa Claude Desktop 管理记录损坏，拒绝操作")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("cpa Claude Desktop 管理记录包含多份数据，拒绝操作")
	}
	return &j, nil
}
func writeJournal(s *store.Store, j *journal) error {
	if err := store.EnsureDir(s.Path("claudedesktop")); err != nil {
		return err
	}
	return store.WriteJSON(journalPath(s), j)
}
func validateJournal(j *journal) error {
	if j.Version != journalVersion || !filepath.IsAbs(j.Root) || j.Root != filepath.Clean(j.Root) || j.ApplyID == "" || j.ProfileID == "" || j.ProfileName == "" || len(j.EntryID) != 36 || uuid(j.EntryID) != j.EntryID || (j.Status != "pending" && j.Status != "committed") || (j.Action != "apply" && j.Action != "restore") || len(j.Files) != 4 {
		return fmt.Errorf("cpa Claude Desktop 管理记录无效，拒绝操作")
	}
	if j.Status == "committed" && (j.Action != "apply" || j.PriorProfileID != "") {
		return fmt.Errorf("Desktop 管理记录的完成状态无效")
	}
	for _, c := range strings.ReplaceAll(j.EntryID, "-", "") {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return fmt.Errorf("Desktop 管理记录中的配置 ID 无效")
		}
	}
	allowed := map[string][]string{primaryModeFile: {"deploymentMode"}, thirdPartyModeFile: {"deploymentMode"}, metaFile: {"appliedId", entryField, "hybridPointer"}, configFile(j): configFields}
	for name, keys := range allowed {
		r := j.Files[name]
		if r == nil || r.Original.Data == nil || len(r.Fields) != len(keys) {
			return fmt.Errorf("Desktop 管理记录中的文件字段无效")
		}
		if j.Status == "committed" && (len(r.Prior) != 0 || len(r.Goal) != 0) {
			return fmt.Errorf("Desktop 已完成记录仍含待完成字段，拒绝操作")
		}
		if r.Original.Mode & ^uint32(0777) != 0 || (!r.Original.Exists && (r.Original.Mode != 0 || len(r.Original.Data) != 0)) {
			return fmt.Errorf("Desktop 管理记录中的原文件状态无效")
		}
		if name == configFile(j) && r.Original.Exists {
			return fmt.Errorf("Desktop 自有配置项不应有接管前文件")
		}
		if err := validateShape(name, r.Original.Data); err != nil {
			return fmt.Errorf("Desktop 管理记录中的原配置格式无效：%w", err)
		}
		for _, key := range keys {
			ch, ok := r.Fields[key]
			if !ok {
				return fmt.Errorf("Desktop 管理记录缺少受管字段")
			}
			original, err := field(r.Original.Data, key, j.EntryID)
			if err != nil || !same(original, ch.Original) {
				return fmt.Errorf("Desktop 管理记录的原字段与基线不匹配")
			}
			if j.Status == "pending" {
				if _, ok := r.Prior[key]; !ok {
					return fmt.Errorf("Desktop 中断记录缺少原状态")
				}
				goal, ok := r.Goal[key]
				if !ok {
					return fmt.Errorf("Desktop 中断记录缺少目标状态")
				}
				expected := r.Fields[key].Applied
				if j.Action == "restore" {
					expected = r.Fields[key].Original
				}
				if !same(goal, expected) {
					return fmt.Errorf("Desktop 中断记录的目标字段无效")
				}
			}
		}
		if j.Status == "pending" && (len(r.Prior) != len(keys) || len(r.Goal) != len(keys)) {
			return fmt.Errorf("Desktop 中断记录字段无效")
		}
	}
	return nil
}

func readAll(j *journal) (map[string]snapshot, error) {
	out := map[string]snapshot{}
	for _, name := range fileNames(j) {
		snap, err := readFile(j.Root, name)
		if err != nil {
			return nil, err
		}
		if err := validateShape(name, snap.Data); err != nil {
			return nil, err
		}
		out[name] = snap
	}
	meta := out[metaFile].Data
	if raw, exists := meta["appliedId"]; exists && raw != "" && raw != j.EntryID {
		id := raw.(string)
		if !validEntryID(id) {
			return nil, fmt.Errorf("Desktop 活动配置 ID 无法识别")
		}
		active, err := readFile(j.Root, "Claude-3p/configLibrary/"+id+".json")
		if err != nil {
			return nil, err
		}
		if !active.Exists {
			return nil, fmt.Errorf("Desktop 活动配置项不存在，拒绝覆盖")
		}
	}
	return out, nil
}
func validEntryID(id string) bool {
	if len(id) != 36 || uuid(id) != id {
		return false
	}
	for _, c := range strings.ReplaceAll(id, "-", "") {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func validateShape(name string, data map[string]any) error {
	if name == metaFile {
		if v, ok := data["appliedId"]; ok {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("Desktop 活动配置 ID 格式无效")
			}
		}
		if raw, ok := data["entries"]; ok {
			entries, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("Desktop 配置项列表必须是数组")
			}
			seen := map[string]bool{}
			for _, raw := range entries {
				e, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("Desktop 配置项格式无效")
				}
				id, ok := e["id"].(string)
				if !ok || !validEntryID(id) || seen[id] {
					return fmt.Errorf("Desktop 配置项 ID 缺失或重复")
				}
				if _, ok := e["name"].(string); !ok {
					return fmt.Errorf("Desktop 配置项名称无效")
				}
				seen[id] = true
			}
		}
		if raw, ok := data["hybridPointer"]; ok {
			p, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("Desktop 混合配置指针格式无效")
			}
			if _, ok := p["bootstrapUrl"].(string); !ok {
				return fmt.Errorf("Desktop 混合配置地址格式无效")
			}
		}
	}
	if name == primaryModeFile || name == thirdPartyModeFile {
		if v, ok := data["deploymentMode"]; ok && v != "1p" && v != "3p" {
			return fmt.Errorf("Desktop deploymentMode 无法识别")
		}
	}
	return nil
}
func field(data map[string]any, key, id string) (value, error) {
	if key != entryField {
		v, ok := data[key]
		return value{ok, v}, nil
	}
	raw, ok := data["entries"]
	if !ok {
		return value{}, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return value{}, fmt.Errorf("Desktop 配置项列表必须是数组")
	}
	result := value{}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return value{}, fmt.Errorf("Desktop 配置项格式无效")
		}
		if entry["id"] == id {
			if result.Present {
				return value{}, fmt.Errorf("Desktop 配置项 ID 重复")
			}
			result = value{true, entry}
		}
	}
	return result, nil
}
func setField(data map[string]any, key, id string, v value) error {
	if key != entryField {
		if v.Present {
			data[key] = v.Data
		} else {
			delete(data, key)
		}
		return nil
	}
	var entries []any
	if raw, ok := data["entries"]; ok {
		var valid bool
		entries, valid = raw.([]any)
		if !valid {
			return fmt.Errorf("Desktop 配置项列表必须是数组")
		}
	}
	out := []any{}
	found := false
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("Desktop 配置项格式无效")
		}
		if entry["id"] == id {
			found = true
			if v.Present {
				out = append(out, v.Data)
			}
		} else {
			out = append(out, entry)
		}
	}
	if !found && v.Present {
		out = append(out, v.Data)
	}
	if len(out) == 0 {
		delete(data, "entries")
	} else {
		data["entries"] = out
	}
	return nil
}
func equal(a, b any) bool {
	x, e := json.Marshal(a)
	if e != nil {
		return false
	}
	y, e := json.Marshal(b)
	return e == nil && bytes.Equal(x, y)
}
func same(a, b value) bool { return a.Present == b.Present && (!a.Present || equal(a.Data, b.Data)) }
func matches(data map[string]any, fields map[string]value, id string) bool {
	for key, v := range fields {
		actual, err := field(data, key, id)
		if err != nil || !same(actual, v) {
			return false
		}
	}
	return true
}
func applied(r *fileRecord) map[string]value {
	out := map[string]value{}
	for key, c := range r.Fields {
		out[key] = c.Applied
	}
	return out
}
func checkApplied(j *journal, current map[string]snapshot) error {
	for name, r := range j.Files {
		if !matches(current[name].Data, applied(r), j.EntryID) {
			return fmt.Errorf("Claude Desktop 受管字段已被修改，拒绝整体操作：%s", name)
		}
	}
	return nil
}

// resume 在任何写入前检查所有文件。允许单个文件处在事务前或目标状态，以续写跨文件中断。
func resume(s *store.Store, j *journal, hook transactionHook) error {
	if err := validateJournal(j); err != nil {
		return err
	}
	current, err := readAll(j)
	if err != nil {
		return err
	}
	for name, r := range j.Files {
		if !matches(current[name].Data, r.Prior, j.EntryID) && !matches(current[name].Data, r.Goal, j.EntryID) {
			return fmt.Errorf("Desktop 中断操作的受管字段冲突，拒绝整体续写：%s", name)
		}
	}
	if hook != nil {
		if err := hook("before-files", ""); err != nil {
			return err
		}
	}
	for _, name := range fileNames(j) {
		r := j.Files[name]
		latest, err := readFile(j.Root, name)
		if err != nil {
			return err
		}
		if !matches(latest.Data, r.Prior, j.EntryID) && !matches(latest.Data, r.Goal, j.EntryID) {
			return fmt.Errorf("Desktop 受管字段在写入期间发生变化，拒绝覆盖")
		}
		for key, v := range r.Goal {
			if err := setField(latest.Data, key, j.EntryID, v); err != nil {
				return err
			}
		}
		if j.Action == "restore" && name == metaFile {
			if _, exists := r.Original.Data["entries"]; exists {
				if _, exists := latest.Data["entries"]; !exists {
					latest.Data["entries"] = []any{}
				}
			}
		}
		remove := j.Action == "restore" && !r.Original.Exists && len(latest.Data) == 0
		mode := uint32(0600)
		if j.Action == "restore" && r.Original.Exists {
			mode = r.Original.Mode
		}
		if err := writeFile(j.Root, name, latest.Data, mode, remove, hook); err != nil {
			return err
		}
		back, err := readFile(j.Root, name)
		if err != nil {
			return err
		}
		if !matches(back.Data, r.Goal, j.EntryID) || !equal(back.Data, latest.Data) {
			return fmt.Errorf("Desktop 配置回读校验失败：%s", name)
		}
		if hook != nil {
			if err := hook("after-file", name); err != nil {
				return err
			}
		}
	}
	if j.Action == "restore" {
		if err := store.CheckFile(journalPath(s)); err != nil {
			return err
		}
		if err := os.Remove(journalPath(s)); err != nil {
			return err
		}
		return syncDir(filepath.Dir(journalPath(s)))
	}
	j.Status = "committed"
	j.PriorProfileID = ""
	for _, r := range j.Files {
		r.Prior = nil
		r.Goal = nil
	}
	return writeJournal(s, j)
}
func safeParents(root, name string, create bool) error {
	if err := rejectSymlinkAncestors(root); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Desktop 配置目录不能是符号链接或非目录")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if create {
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
	} else {
		return nil
	}
	current := root
	for _, part := range strings.Split(filepath.Dir(filepath.FromSlash(name)), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if create {
				if err := os.Mkdir(current, 0700); err != nil {
					return err
				}
				continue
			}
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Desktop 配置路径含符号链接或非目录：%s", current)
		}
	}
	return nil
}
func readFile(root, name string) (snapshot, error) {
	out := snapshot{Data: map[string]any{}}
	if err := safeParents(root, name, false); err != nil {
		return out, err
	}
	p := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return out, fmt.Errorf("拒绝非普通 Desktop 配置文件或符号链接：%s", name)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return out, fmt.Errorf("Desktop 配置文件必须属于当前用户且不能有硬链接：%s", name)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out.Data); err != nil || out.Data == nil {
		return out, fmt.Errorf("Desktop 配置 JSON 格式无效，拒绝覆盖：%s", name)
	}
	out.Exists = true
	out.Mode = uint32(info.Mode().Perm())
	return out, nil
}
func writeFile(root, name string, data map[string]any, mode uint32, remove bool, hook transactionHook) error {
	if err := safeParents(root, name, true); err != nil {
		return err
	}
	p := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Lstat(p)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝非普通 Desktop 配置文件或符号链接")
		}
		if err := os.Chmod(p, 0600); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if hook != nil {
		if err := hook("prepared", name); err != nil {
			return err
		}
	}
	if remove {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDir(filepath.Dir(p))
	}
	if err := store.WriteJSON(p, data); err != nil {
		return err
	}
	if mode != 0 && mode != 0600 {
		if err := os.Chmod(p, os.FileMode(mode).Perm()); err != nil {
			return err
		}
	}
	return nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func isDefaultRoot(root, input string) bool {
	if input == "" {
		return true
	}
	u, err := user.Current()
	return err == nil && normalizeSystemAliases(root) == normalizeSystemAliases(filepath.Join(u.HomeDir, "Library", "Application Support"))
}
func checkEnvironment(root string, defaultRoot bool) error {
	for _, dir := range []string{"Claude", "Claude-3p"} {
		if err := safeParents(root, dir+"/configLibrary/_meta.json", false); err != nil {
			return err
		}
		for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
			if _, err := os.Lstat(filepath.Join(root, dir, name)); err == nil {
				return fmt.Errorf("Claude Desktop 仍在运行或存在实例锁，请完整退出后重试")
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if defaultRoot {
			paths := []string{"/Library/Managed Preferences/com.anthropic.claudefordesktop.plist"}
			u, err := user.Current()
			if err != nil {
				return fmt.Errorf("无法确认 Desktop 组织策略来源")
			}
			paths = append(paths, filepath.Join("/Library/Managed Preferences", u.Username, "com.anthropic.claudefordesktop.plist"))
			if os.Getenv("CLAUDE_E2E_MANAGED_PLIST") != "" {
				return fmt.Errorf("Desktop 组织策略来源已被环境覆盖，拒绝接管")
			}
			if err := rejectManagedPaths(paths); err != nil {
				return err
			}
		}
		data, err := exec.Command("/bin/ps", "-axo", "command=").Output()
		if err != nil {
			return fmt.Errorf("无法确认 Claude Desktop 已退出：%w", err)
		}
		return rejectRunningProcesses(string(data), root, defaultRoot)
	}
	return nil
}
func rejectRunningProcesses(data, root string, defaultRoot bool) error {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, ".app/Contents/MacOS/Claude") {
			continue
		}
		if defaultRoot {
			return fmt.Errorf("Claude Desktop 正在运行，请完整退出后重试")
		}
		for _, dir := range []string{"Claude", "Claude-3p"} {
			if matchesUserDataFlag(line, filepath.Join(root, dir)) {
				return fmt.Errorf("Claude Desktop 正在使用该配置目录，请完整退出后重试")
			}
		}
	}
	return nil
}

// ps 保留启动参数的写法，因而也比较 macOS 系统别名的等价前缀。
func matchesUserDataFlag(command, path string) bool {
	path = normalizeSystemAliases(path)
	candidates := []string{path}
	if runtime.GOOS == "darwin" {
		for _, prefix := range []string{"/private/tmp", "/private/var"} {
			if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
				candidates = append(candidates, strings.TrimPrefix(path, "/private"))
			}
		}
	}
	for _, candidate := range candidates {
		flag := "--user-data-dir=" + candidate
		if strings.HasSuffix(command, flag) || strings.Contains(command, flag+" ") {
			return true
		}
	}
	return false
}

// 不尝试合并或覆盖组织策略，即使策略文件损坏或只提供部分配置也交由管理员处理。
func rejectManagedPaths(paths []string) error {
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("检测到 Claude Desktop 组织策略，拒绝接管本地配置")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("无法确认 Desktop 组织策略：%w", err)
		}
	}
	return nil
}
