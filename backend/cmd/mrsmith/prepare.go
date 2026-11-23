package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// prepareBackupDir runs once as root before the panel starts (see
// docker-compose.yml) and hands the backup folder to the panel's user. It
// never fails: a folder problem must not keep the panel from starting.
func prepareBackupDir(dir string) {
	const uid, gid = 65532, 65532 // the user the image runs as
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "mrsmith: папки", dir, "нет, бэкапы будут недоступны")
		return
	}
	_ = os.Chmod(dir, 0o700)
	if err := os.Chown(dir, uid, gid); err != nil {
		fmt.Fprintln(os.Stderr, "mrsmith: не удалось выдать права на", dir+":", err)
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Type().IsRegular() {
			p := filepath.Join(dir, e.Name())
			_ = os.Chmod(p, 0o600)
			_ = os.Chown(p, uid, gid)
		}
	}
	fmt.Println("папка", dir, "готова для бэкапов")
}
