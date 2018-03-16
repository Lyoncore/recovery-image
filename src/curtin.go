package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"

	rplib "github.com/Lyoncore/ubuntu-custom-recovery/src/rplib"
)

type CurtinConf struct {
	Install struct {
		SaveInstallConfig string `yaml:"save_install_config,omitempty"`
		SaveInstallLog    string `yaml:"save_install_log,omitempty"`
		Target            string `yaml:"target"`
		Unmount           string `yaml:"unmount"`
	}
	PartitionCmds struct {
		Builtin string `yaml:"builtin"`
	} `yaml:"partitioning_commands"`
	Sources struct {
		Rofs string `yaml:"rofs"`
	}
	Storage struct {
		Config  []StorageConfigContent `yaml:"config"`
		Version int                    `yaml:"version"`
	}
	Reporting map[string]ReportingContent `yaml:"reporting"`
	Verbosity int                         `yaml: "verbosity,omitempty"`
}

type ReportingContent struct {
	Type string `yaml:"type"`
}

type StorageConfigContent struct {
	ID         string `yaml:"id"`
	Type       string `yaml:"type"`
	Ptable     string `yaml:"ptable,omitempty"`
	Path       string `yaml:"path,omitempty"`
	GrubDevice bool   `yaml:"grub_device,omitempty"`
	Preserve   bool   `yaml:"preserve,omitempty"`
	Number     int    `yaml:"number,omitempty"`
	Device     string `yaml:"device,omitempty"`
	Size       int    `yaml:"size,omitempty"`
	Flag       string `yaml:"flag,omitempty"`
	Fstype     string `yaml:"fstype,omitempty"`
	Volume     string `yaml:"volume,omitempty"`
}

const CURTIN_CONF_FILE = "/tmp/curtin-recovery-cfg.yaml"
const CURTIN_DEFAULT_CONF_CONTENTS = `
partitioning_commands:
  builtin: curtin block-meta custom
install:
  save_install_config: /var/log/recovery/curtin-recovery-cfg.yaml
  save_install_log: /var/log/recovery/curtin-recovery.log
  target: /target
  unmount: disabled
reporting:
  recovery-bin:
    type: journald
sources:
  rofs: 'cp:///rofs'
storage:
  config:
  - {id: disk-0, type: disk, ptable: gpt, path: ###DISK_PATH###, grub_device: true, preserve: true}
  - {id: part-recovery, type: partition, number: 1, device: disk-0, size: ###RECO_PART_SIZE###, preserve: true}
  - {id: part-boot, type: partition, number: 2, device: disk-0, size: ###BOOT_PART_SIZE###, flag: boot, preserve: true}
  - {id: part-rootfs, type: partition, number: 3, device: disk-0, size: ###ROOTFS_PART_SIZE###, preserve: true}
  - {id: fs-boot, type: format, fstype: fat32, volume: part-boot, preserve: true}
  - {id: fs-rootfs, type: format, fstype: ext4, volume: part-rootfs, preserve: true}
  - {id: mount-rootfs, type: mount, device: fs-rootfs, path: /, preserve: true}
  - {id: mount-boot, type: mount, device: fs-boot, path: /boot/efi, preserve: true}
  version: 1
verbosity: 3
`

func envForUbuntuClassicCurtin() error {
	const CURTIN_RECO_ROOT_DIR = "/cdrom"
	if _, err := os.Stat(RECO_ROOT_DIR); os.IsNotExist(err) {
		if err = os.Mkdir(RECO_ROOT_DIR, 0755); err != nil {
			log.Println("create dir ", RECO_ROOT_DIR, "failed", err.Error())
			return err
		}
	}

	log.Printf("bind mount the %s to %s", CURTIN_RECO_ROOT_DIR, RECO_ROOT_DIR)
	if err := syscall.Mount(CURTIN_RECO_ROOT_DIR, RECO_ROOT_DIR, "", syscall.MS_BIND, ""); err != nil {
		log.Println("bind mount failed, ", err.Error())
		return err
	}

	return nil
}

func generateCurtinConf(parts *Partitions) error {
	var curtinCfg string
	curtinCfg = strings.Replace(CURTIN_DEFAULT_CONF_CONTENTS, "###DISK_PATH###", parts.TargetDevPath, -1)
	curtinCfg = strings.Replace(curtinCfg, "###RECO_PART_SIZE###", strconv.FormatInt(int64(configs.Recovery.RecoverySize*1024*1024), 10), -1)
	if configs.Configs.BootSize > 0 {
		curtinCfg = strings.Replace(curtinCfg, "###BOOT_PART_SIZE###", strconv.FormatInt(int64(configs.Configs.BootSize*1024*1024), 10), -1)
	} else {
		return fmt.Errorf("Invalid boot size configured in config.yaml")
	}
	if configs.Configs.RootfsSize > 0 {
		curtinCfg = strings.Replace(curtinCfg, "###ROOTFS_PART_SIZE###", strconv.FormatInt(int64(configs.Configs.RootfsSize*1024*1024), 10), -1)
	} else if configs.Configs.RootfsSize < 0 {
		// using the remaining free space for rootfs
		rootsize := parts.TargetSize - int64(configs.Configs.BootSize*1024*1024)
		if configs.Configs.Swap == true && configs.Configs.SwapSize > 0 {
			rootsize -= int64(configs.Configs.SwapSize * 1024 * 1024)
		}
		curtinCfg = strings.Replace(curtinCfg, "###ROOTFS_PART_SIZE###", strconv.FormatInt(int64(rootsize), 10), -1)
	} else {
		return fmt.Errorf("Invalid rootfs size configured in config.yaml")
	}

	f, err := os.Create(CURTIN_CONF_FILE)
	if err != nil {
		return fmt.Errorf("Create curtin conf file failed. File: %s", CURTIN_CONF_FILE)
	}
	defer f.Close()

	if _, err := f.WriteString(curtinCfg); err != nil {
		return fmt.Errorf("Write curtin conf file failed. File: %s", CURTIN_CONF_FILE)
	}

	f.Sync()
	return nil
}

func runCurtin() error {
	rplib.Shellexec("curtin", "--showtrace", "-c", CURTIN_CONF_FILE, "install")
	return nil
}

func writeCloudInitConf() error {
	return nil
}

// 1. generate curtin config
// 2. call curtin
// 3. write cloud-init files
// 4. set grub
// 5. set boot entry (efibootmgr)
