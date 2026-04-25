package export

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Writer 负责将邮箱列表写入 txt 文件（分片）
type Writer struct {
	OutputDir    string // 输出目录
	LinesPerFile int    // 每个文件最大行数
}

// WriteResult 写入结果
type WriteResult struct {
	TotalEmails   int      // 本次写入的邮箱总数
	NewEmails     int      // 新增不重复的邮箱数
	DuplicateSkip int      // 跳过的重复邮箱数
	FilesCreated  []string // 创建/追加的文件列表
}

// WriteEmails 将邮箱列表写入 txt 文件
// 会加载已有文件中的邮箱进行去重，然后追加或创建新文件
func (w *Writer) WriteEmails(emails []string) (WriteResult, error) {
	result := WriteResult{TotalEmails: len(emails)}

	if len(emails) == 0 {
		return result, nil
	}

	if err := os.MkdirAll(w.OutputDir, 0755); err != nil {
		return result, fmt.Errorf("创建输出目录失败: %w", err)
	}

	linesPerFile := w.LinesPerFile
	if linesPerFile <= 0 {
		linesPerFile = 50000
	}

	// 加载已有邮箱进行全局去重
	existing, lastFile, lastCount := w.loadExisting()

	// 过滤重复
	var newEmails []string
	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			continue
		}
		if _, exists := existing[email]; exists {
			result.DuplicateSkip++
			continue
		}
		existing[email] = struct{}{}
		newEmails = append(newEmails, email)
	}

	result.NewEmails = len(newEmails)
	if len(newEmails) == 0 {
		return result, nil
	}

	// 追加到最后一个文件或创建新文件
	idx := 0
	fileNum := lastFile
	currentCount := lastCount

	for idx < len(newEmails) {
		// 如果当前文件已满，创建新文件
		if currentCount >= linesPerFile {
			fileNum++
			currentCount = 0
		}

		// 写入时用基础文件名（不带条数）
		baseFileName := fmt.Sprintf("emails_%03d.txt", fileNum)
		basePath := filepath.Join(w.OutputDir, baseFileName)

		// 如果存在带条数的旧文件，先找到它
		actualPath := w.findActualFile(fileNum)
		if actualPath == "" {
			actualPath = basePath
		}

		f, err := os.OpenFile(actualPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return result, fmt.Errorf("打开文件 %s 失败: %w", actualPath, err)
		}

		bw := bufio.NewWriter(f)
		wrote := 0
		for idx < len(newEmails) && currentCount < linesPerFile {
			bw.WriteString(newEmails[idx])
			bw.WriteString("\n")
			idx++
			currentCount++
			wrote++
		}
		bw.Flush()
		f.Close()

		if wrote > 0 {
			// 重命名文件：emails_001_50000条.txt
			newName := fmt.Sprintf("emails_%03d_%d条.txt", fileNum, currentCount)
			newPath := filepath.Join(w.OutputDir, newName)
			if actualPath != newPath {
				os.Rename(actualPath, newPath)
			}
			result.FilesCreated = append(result.FilesCreated, newName)
		}
	}

	return result, nil
}

// classifyEmail 按域名分类
func classifyEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return "other"
	}
	domain := strings.ToLower(email[at+1:])

	// Gmail
	if domain == "gmail.com" {
		return "gmail"
	}

	// Yahoo Japan
	if domain == "yahoo.co.jp" || domain == "ymail.ne.jp" || domain == "ybb.ne.jp" {
		return "yahoo_jp"
	}

	// docomo
	if domain == "docomo.ne.jp" || domain == "docomo.jp" || domain == "mopera.net" {
		return "docomo"
	}

	// au / KDDI
	if domain == "ezweb.ne.jp" || domain == "au.com" || domain == "biz.au.com" ||
		strings.HasSuffix(domain, ".dion.ne.jp") {
		return "au"
	}

	// SoftBank
	if domain == "softbank.ne.jp" || domain == "i.softbank.jp" ||
		domain == "vodafone.ne.jp" || domain == "disney.ne.jp" || domain == "willcom.com" {
		return "softbank"
	}

	return "other"
}

// WriteCategorized 按域名分类写入文件（gmail/yahoo_jp/docomo/au/softbank/other）
// 替代 WriteEmails：跨所有分类全局去重，返回统计结果
func (w *Writer) WriteCategorized(emails []string) (WriteResult, error) {
	result := WriteResult{TotalEmails: len(emails)}

	if len(emails) == 0 {
		return result, nil
	}

	if err := os.MkdirAll(w.OutputDir, 0755); err != nil {
		return result, fmt.Errorf("创建输出目录失败: %w", err)
	}

	catNames := map[string]string{
		"gmail":    "Gmail",
		"yahoo_jp": "Yahoo_JP",
		"docomo":   "Docomo",
		"au":       "AU_KDDI",
		"softbank": "SoftBank",
		"other":    "其他",
	}

	// 加载所有分类文件中已有的邮箱做全局去重
	globalExisting := make(map[string]struct{})
	for _, label := range catNames {
		pattern := filepath.Join(w.OutputDir, label+"_*.txt")
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if f, err := os.Open(m); err == nil {
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					e := strings.TrimSpace(scanner.Text())
					if e != "" {
						globalExisting[e] = struct{}{}
					}
				}
				f.Close()
			}
		}
	}

	// 去重 + 分类
	groups := make(map[string][]string)
	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			continue
		}
		if _, exists := globalExisting[email]; exists {
			result.DuplicateSkip++
			continue
		}
		globalExisting[email] = struct{}{}
		cat := classifyEmail(email)
		groups[cat] = append(groups[cat], email)
		result.NewEmails++
	}

	if result.NewEmails == 0 {
		return result, nil
	}

	// 写入各分类文件
	for cat, list := range groups {
		if len(list) == 0 {
			continue
		}

		label := catNames[cat]
		if label == "" {
			label = cat
		}

		// 找已有文件：合并所有同分类文件为一个
		pattern := filepath.Join(w.OutputDir, label+"_*.txt")
		matches, _ := filepath.Glob(pattern)
		var actualPath string
		existingCount := 0

		if len(matches) > 0 {
			sort.Strings(matches)
			// 以第一个文件为基准，把其他文件内容合并进来然后删除
			actualPath = matches[0]
			if len(matches) > 1 {
				mergeF, err := os.OpenFile(actualPath, os.O_WRONLY|os.O_APPEND, 0644)
				if err == nil {
					for _, extra := range matches[1:] {
						data, err := os.ReadFile(extra)
						if err == nil {
							mergeF.Write(data)
						}
						os.Remove(extra)
					}
					mergeF.Close()
				}
			}
			// 数合并后行数
			if f, err := os.Open(actualPath); err == nil {
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					if strings.TrimSpace(scanner.Text()) != "" {
						existingCount++
					}
				}
				f.Close()
			}
		} else {
			actualPath = filepath.Join(w.OutputDir, fmt.Sprintf("%s_0.txt", label))
		}

		// 追加写入
		f, err := os.OpenFile(actualPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			continue
		}
		bw := bufio.NewWriter(f)
		for _, e := range list {
			bw.WriteString(e)
			bw.WriteString("\n")
		}
		bw.Flush()
		f.Close()

		// 重命名带条数，先删除可能存在的同名目标文件
		totalCount := existingCount + len(list)
		newName := filepath.Join(w.OutputDir, fmt.Sprintf("%s_%d条.txt", label, totalCount))
		if actualPath != newName {
			os.Remove(newName) // Windows 下 Rename 前需要先删目标
			os.Rename(actualPath, newName)
		}
		result.FilesCreated = append(result.FilesCreated, filepath.Base(newName))
	}

	return result, nil
}

// findActualFile 查找某个编号的实际文件（可能带条数后缀）
func (w *Writer) findActualFile(fileNum int) string {
	pattern := filepath.Join(w.OutputDir, fmt.Sprintf("emails_%03d*.txt", fileNum))
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// loadExisting 加载输出目录中已有的邮箱，返回去重 set、最后文件编号、最后文件行数
func (w *Writer) loadExisting() (map[string]struct{}, int, int) {
	existing := make(map[string]struct{})

	files, err := filepath.Glob(filepath.Join(w.OutputDir, "emails_*.txt"))
	if err != nil || len(files) == 0 {
		return existing, 1, 0
	}

	// 按文件名排序
	sort.Strings(files)

	lastFileNum := 1
	lastCount := 0

	for _, f := range files {
		base := filepath.Base(f)
		// 跳过 merged 文件
		if strings.Contains(base, "merged") {
			continue
		}
		// 提取文件编号：emails_001.txt 或 emails_001_50000条.txt → 1
		numStr := strings.TrimPrefix(base, "emails_")
		// 取第一个 _ 或 . 之前的数字
		if idx := strings.IndexAny(numStr, "_."); idx >= 0 {
			numStr = numStr[:idx]
		}
		num, _ := strconv.Atoi(numStr)
		if num > lastFileNum {
			lastFileNum = num
		}

		file, err := os.Open(f)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		lineCount := 0
		for scanner.Scan() {
			email := strings.TrimSpace(scanner.Text())
			if email != "" {
				existing[email] = struct{}{}
				lineCount++
			}
		}
		file.Close()

		if num == lastFileNum {
			lastCount = lineCount
		}
	}

	return existing, lastFileNum, lastCount
}

// MergeDedup 合并所有 emails_*.txt 去重输出到 emails_merged.txt
// 返回：原始总行数、去重后数量、去掉的重复数
func (w *Writer) MergeDedup() (int, int, error) {
	files, err := filepath.Glob(filepath.Join(w.OutputDir, "emails_*.txt"))
	if err != nil || len(files) == 0 {
		return 0, 0, fmt.Errorf("没有找到邮箱文件")
	}

	seen := make(map[string]struct{})
	var unique []string
	totalRaw := 0

	sort.Strings(files)
	for _, f := range files {
		// 跳过已有的 merged 文件
		if strings.Contains(filepath.Base(f), "merged") {
			continue
		}
		file, err := os.Open(f)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			email := strings.ToLower(strings.TrimSpace(scanner.Text()))
			if email == "" {
				continue
			}
			totalRaw++
			if _, exists := seen[email]; !exists {
				seen[email] = struct{}{}
				unique = append(unique, email)
			}
		}
		file.Close()
	}

	if len(unique) == 0 {
		return 0, 0, fmt.Errorf("没有有效邮箱")
	}

	outPath := filepath.Join(w.OutputDir, "emails_merged.txt")
	f, err := os.Create(outPath)
	if err != nil {
		return 0, 0, fmt.Errorf("创建合并文件失败: %w", err)
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	for _, email := range unique {
		bw.WriteString(email)
		bw.WriteString("\n")
	}
	bw.Flush()

	return totalRaw, len(unique), nil
}

// CountExistingEmails 统计已导出的邮箱总数（从分类文件 + 旧格式文件）
func (w *Writer) CountExistingEmails() int {
	total := 0
	seen := make(map[string]bool)

	// 分类文件
	catLabels := []string{"Gmail", "Yahoo_JP", "Docomo", "AU_KDDI", "SoftBank", "其他"}
	for _, label := range catLabels {
		matches, _ := filepath.Glob(filepath.Join(w.OutputDir, label+"_*.txt"))
		for _, m := range matches {
			seen[m] = true
			total += countFileLines(m)
		}
	}

	// 旧格式 emails_*.txt（兼容）
	files, _ := filepath.Glob(filepath.Join(w.OutputDir, "emails_*.txt"))
	for _, f := range files {
		if seen[f] || strings.Contains(filepath.Base(f), "merged") {
			continue
		}
		total += countFileLines(f)
	}

	return total
}

func countFileLines(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
}
