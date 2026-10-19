// Test Google Photos Mobile filesystem interface
package gphotosmobile_test

import (
	"testing"

	"github.com/user/rclone-gphotos-oot/backend/gphotosmobile"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against the remote
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName: "TestGPhotosMobile:",
		NilObject:  (*gphotosmobile.Object)(nil),
	})
}
