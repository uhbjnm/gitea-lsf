package gateway

import (
	"strings"
	"testing"
)

func TestAttachmentContentDisposition(t *testing.T) {
	disposition := attachmentContentDisposition("setup-win-x64.zip")
	if disposition != `attachment; filename="setup-win-x64.zip"; filename*=UTF-8''setup-win-x64.zip` {
		t.Fatalf("disposition = %q", disposition)
	}

	disposition = attachmentContentDisposition("安装包.zip")
	if !strings.Contains(disposition, `filename="___.zip"`) || !strings.Contains(disposition, "filename*=UTF-8''%E5%AE%89%E8%A3%85%E5%8C%85.zip") {
		t.Fatalf("unicode disposition = %q", disposition)
	}
}
