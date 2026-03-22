package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	log "xbot/logger"
	"xbot/llm"
)

// SandboxCleanupTool 压缩用户沙箱镜像：stop → export → import → restart
// 用于手动清理沙箱的镜像层和磁盘占用
type SandboxCleanupTool struct{}

func (t *SandboxCleanupTool) Name() string {
	return "sandbox_cleanup"
}

func (t *SandboxCleanupTool) Description() string {
	return `Compact the user's sandbox: stop the container, export+import to a single-layer image, then restart.
This reclaims disk space by eliminating accumulated Docker layers.
No parameters needed — operates on the current user's sandbox.`
}

func (t *SandboxCleanupTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{}
}

func (t *SandboxCleanupTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	// 忽略 input，不需要参数
	_ = json.RawMessage(input)

	sb := GetSandbox()
	ds, ok := sb.(*dockerSandbox)
	if !ok {
		return &ToolResult{
			Summary: "Sandbox cleanup is only available in Docker sandbox mode.",
			Detail:  fmt.Sprintf("Current sandbox type: %s", sb.Name()),
		}, nil
	}

	userID := ""
	if toolCtx != nil {
		userID = toolCtx.OriginUserID
	}
	if userID == "" {
		return &ToolResult{Summary: "Cannot determine user ID for cleanup."}, nil
	}

	report, err := ds.compact(userID)
	if err != nil {
		return &ToolResult{
			Summary: fmt.Sprintf("Sandbox cleanup failed: %s", err),
		}, nil
	}

	return &ToolResult{
		Summary: "Sandbox cleanup completed.",
		Detail:  report,
	}, nil
}

// compact 压缩指定用户的沙箱：stop → export+import → rm old → restart
// 返回操作报告
func (s *dockerSandbox) compact(userID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.containers[userID]
	if !ok || !c.started {
		return "", fmt.Errorf("no running sandbox found for user %s", userID)
	}

	containerName := c.name
	var report strings.Builder

	// 0. 获取压缩前的镜像大小
	userImage := userImageName(userID)
	oldSize := dockerImageSize(userImage)
	if oldSize == "" {
		oldSize = dockerImageSize(s.image)
	}
	report.WriteString(fmt.Sprintf("Container: %s\n", containerName))
	report.WriteString(fmt.Sprintf("Image before: %s (%s)\n", userImage, oldSize))

	// 1. Stop 容器
	log.Infof("[compact] Stopping container %s", containerName)
	report.WriteString("Step 1: stop container... ")
	if err := dockerRun(dockerCmdTimeout, "stop", "-t", "1", containerName); err != nil {
		report.WriteString(fmt.Sprintf("FAILED: %v\n", err))
		return report.String(), fmt.Errorf("failed to stop container: %w", err)
	}
	report.WriteString("OK\n")

	// 2. Export + Import（强制执行，不检查 dirty）
	log.Infof("[compact] Exporting container %s", containerName)
	report.WriteString("Step 2: export + import... ")
	s.exportImportIfDirty(containerName, userID)
	report.WriteString("OK\n")

	// 3. Remove 旧容器
	log.Infof("[compact] Removing old container %s", containerName)
	report.WriteString("Step 3: remove old container... ")
	if err := dockerRun(dockerCmdTimeout, "rm", "-f", containerName); err != nil {
		log.WithError(err).Warnf("[compact] Failed to remove container %s", containerName)
		report.WriteString(fmt.Sprintf("WARN: %v\n", err))
	} else {
		report.WriteString("OK\n")
	}

	// 4. 清除内存中的容器记录，让下次 Wrap 调用自动用新镜像创建容器
	delete(s.containers, userID)

	// 5. 获取压缩后的镜像大小
	newSize := dockerImageSize(userImage)
	report.WriteString(fmt.Sprintf("Image after: %s (%s)\n", userImage, newSize))

	// 6. 清理残留 tmp 文件
	cleanupStaleTmpFiles()
	report.WriteString("Stale tmp files cleaned.\n")

	log.Infof("[compact] Sandbox cleanup completed for user %s", userID)
	report.WriteString("Done. Container will be recreated on next command.")

	return report.String(), nil
}

// dockerImageSize 获取镜像大小的人类可读字符串
func dockerImageSize(image string) string {
	out, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Size}}", image)
	if err != nil {
		return "unknown"
	}
	sizeStr := strings.TrimSpace(string(out))
	var size int64
	if _, err := fmt.Sscanf(sizeStr, "%d", &size); err != nil {
		return sizeStr
	}
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(size)/(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	default:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	}
}
