package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v2"

	rplib "github.com/Lyoncore/ubuntu-custom-recovery/src/rplib"
)

const (
	CURTIN_INSTALL_TARGET = "/target"
	CURTIN_BOOT_MNT       = CURTIN_INSTALL_TARGET + "/boot/efi"
	NOCLOUDNETDIR         = CURTIN_INSTALL_TARGET + "/var/lib/cloud/seed/nocloud-net/"
	CLOUDMETA             = NOCLOUDNETDIR + "meta-data"
	CLOUDUSER             = NOCLOUDNETDIR + "user-data"
	CLOUD_DSIDENTITY      = CURTIN_INSTALL_TARGET + "/etc/cloud/ds-identify.cfg"
	SUBIQUITY_ANSWERS     = RECO_ROOT_DIR + "recovery/answers.yaml"
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
	Network   struct {
		Config  []NetworkConfigContent `yaml:"config"`
		Version int
	}
	Swap struct {
		Size int `yaml:"size"`
	} `yaml:"swap"`
	Grub struct {
		Updatenvram bool `yaml:"update_nvram"`
	} `yaml:"grub"`
	LateCommands struct {
		RecoveryPost string `yaml:"recovery_post"`
	} `yaml:"late_commands"`
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

type NetworkConfigContent struct {
	Type     string         `yaml:"type"`
	Name     string         `yaml:"name"`
	Mac_addr string         `yaml:"mac_address,omitempty"`
	Subnets  SubnetsContent `yaml:"subnets"`
}

type SubnetsContent struct {
	Type    string `yaml:"type"`
	Address string `yaml:"address,omitempty"`
	Gateway string `yaml:"gateway,omitempty"`
}

const CURTIN_CONF_FILE = "/tmp/curtin-recovery-cfg.yaml"
const CURTIN_DEFAULT_CONF_CONTENTS = `
partitioning_commands:
  builtin: curtin block-meta custom
install:
  save_install_config: /var/log/recovery/curtin-recovery-cfg.yaml
  save_install_log: /var/log/recovery/curtin-recovery.log
  target: ###INSTALL_TARGET###
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
  - {id: part-swap, type: partition, number: ###SWAP_PART_NUMBER###, device: disk-0, size: ###SWAP_PART_SIZE###, preserve: true}
  - {id: part-rootfs, type: partition, number: ###ROOTFS_PART_NUMBER###, device: disk-0, size: ###ROOTFS_PART_SIZE###, preserve: true}
  - {id: fs-boot, type: format, fstype: fat32, volume: part-boot, preserve: true}
  - {id: fs-rootfs, type: format, fstype: ext4, volume: part-rootfs, preserve: true}
  - {id: mount-rootfs, type: mount, device: fs-rootfs, path: /, preserve: true}
  - {id: mount-boot, type: mount, device: fs-boot, path: /boot/efi, preserve: true}
  version: 1
verbosity: 3
swap:
  size: 0
grub:
  update_nvram: False
late_commands:
  recovery_post: /cdrom/recovery/bin/recovery_post.sh
`
const COULD_INIT_DEFALUT_USER_DATA = `#cloud-config
hostname: ###HOSTNAME###
users:
- gecos: ###REALNAME###
  groups: [adm, cdrom, dip, lpadmin, plugdev, sambashare, debian-tor, libvirtd, lxd,
    sudo]
  lock-passwd: false
  name: ###USERNAME###
  passwd: ###PASSWDSALTED###
  shell: /bin/bash
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
	curtinCfg = strings.Replace(curtinCfg, "###INSTALL_TARGET###", CURTIN_INSTALL_TARGET, -1)
	curtinCfg = strings.Replace(curtinCfg, "###RECO_PART_SIZE###", strconv.FormatInt(int64(configs.Recovery.RecoverySize*1024*1024), 10), -1)
	if configs.Configs.BootSize > 0 {
		curtinCfg = strings.Replace(curtinCfg, "###BOOT_PART_SIZE###", strconv.FormatInt(int64(configs.Configs.BootSize*1024*1024), 10), -1)
	} else {
		return fmt.Errorf("Invalid boot size configured in config.yaml")
	}
	if configs.Configs.Swap == true && configs.Configs.SwapSize > 0 {
		curtinCfg = strings.Replace(curtinCfg, "###SWAP_PART_NUMBER###", strconv.FormatInt(int64(parts.Swap_nr), 10), -1)
		curtinCfg = strings.Replace(curtinCfg, "###SWAP_PART_SIZE###", strconv.FormatInt(int64(configs.Configs.SwapSize*1024*1024), 10), -1)
	} else {
		re := regexp.MustCompile("(?m)[\r\n]+^.*part-swap.*$")
		curtinCfg = re.ReplaceAllString(curtinCfg, "")
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
	curtinCfg = strings.Replace(curtinCfg, "###ROOTFS_PART_NUMBER###", strconv.FormatInt(int64(parts.Writable_nr), 10), -1)

	// get network configs
	netdevs := []NetworkDevice{}
	var err error
	if netdevs, err = findNetworkAnswer(SUBIQUITY_ANSWERS); err != nil {
		log.Println("The answers.yaml has wrong network config")
		return err
	}
	curtYaml := CurtinConf{}
	err = yaml.Unmarshal([]byte(curtinCfg), &curtYaml)
	if err != nil {
		log.Println("Curint config format error")
		return err
	}

	curtYaml.Network.Version = 1
	for dev := 0; dev < len(netdevs); dev++ {
		mac := getMacAddr(netdevs[dev].name)
		if mac == "" {
			log.Println("Cannot find mac addr of network interface", netdevs[dev].name)
			continue
		}
		netcfg := NetworkConfigContent{
			Type:     "physical",
			Name:     netdevs[dev].name,
			Mac_addr: mac,

			Subnets: SubnetsContent{
				Type:    netdevs[dev].subnetsType,
				Address: netdevs[dev].address,
				Gateway: netdevs[dev].gateway,
			},
		}
		curtYaml.Network.Config = append(curtYaml.Network.Config, netcfg)
	}

	d, err := yaml.Marshal(&curtYaml)
	if err != nil {
		log.Println("The curtYaml format error ", err)
		return err
	}

	f, err := os.Create(CURTIN_CONF_FILE)
	if err != nil {
		return fmt.Errorf("Create curtin conf file failed. File: %s", CURTIN_CONF_FILE)
	}
	defer f.Close()

	if _, err := f.WriteString(string(d)); err != nil {
		return fmt.Errorf("Write curtin conf file failed. File: %s", CURTIN_CONF_FILE)
	}

	f.Sync()
	return nil
}

func runCurtin() error {
	rplib.Shellexec("curtin", "--showtrace", "-c", CURTIN_CONF_FILE, "install")
	return nil
}

func findAnswer(answersyaml string, head string, item string) (string, error) {
	yamlFile, err := ioutil.ReadFile(answersyaml)
	if err != nil {
		return "", err
	}

	m := make(map[interface{}]interface{})
	err = yaml.Unmarshal(yamlFile, &m)

	for k, v := range m {
		if k == head {
			for a, b := range v.(map[interface{}]interface{}) {
				if a.(string) == item {
					return b.(string), nil
				}
			}
		}
	}
	return "", fmt.Errorf("Answers item not found\n")
}

type NetworkDevice struct {
	name        string
	subnetsType string
	address     string
	gateway     string
}

func findNetworkAnswer(answersyaml string) ([]NetworkDevice, error) {
	netdevs := []NetworkDevice{}

	yamlFile, err := ioutil.ReadFile(answersyaml)
	if err != nil {
		return nil, nil
	}

	m := make(map[interface{}]interface{})
	err = yaml.Unmarshal(yamlFile, &m)

	for k, v := range m {
		if k == "Network" {
			for _, a := range v.([]interface{}) {
				var name, subnetsType, address, gateway string
				for c, d := range a.(map[interface{}]interface{}) {
					if c == "name" {
						name = d.(string)
					} else if c == "subnets" {
						for _, e := range d.([]interface{}) {
							for f, g := range e.(map[interface{}]interface{}) {
								if f == "type" {
									subnetsType = g.(string)
								} else if f == "address" {
									address = g.(string)
								} else if f == "gateway" {
									gateway = g.(string)
								}
							}
						}
					}
				}
				if name != "" && subnetsType != "" {
					dev := NetworkDevice{
						name:        name,
						subnetsType: subnetsType,
						address:     address,
						gateway:     gateway,
					}
					netdevs = append(netdevs, dev)
				}
			}
		}
	}
	return netdevs, nil
}

func getMacAddr(ifname string) string {
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, i := range interfaces {
			if bytes.Compare(i.HardwareAddr, nil) != 0 && i.Name == ifname {
				return i.HardwareAddr.String()
			}
		}
	}
	return ""
}

func writeCloudInitConf(parts *Partitions) error {
	if _, err := os.Stat(NOCLOUDNETDIR); err != nil {
		err := os.MkdirAll(NOCLOUDNETDIR, 0755)
		if err != nil {
			return err
		}
	}

	// write meta-data
	log.Println("writing the cloud-init meta")
	uuid, err := exec.Command("uuidgen").Output()
	if err != nil {
		log.Println("generate uuid failed")
		return err
	}
	uuid_s := strings.TrimSuffix(string(uuid), "\n")
	meta_data_content := fmt.Sprintf("{instance-id: %s}", uuid_s)
	f_meta_data, err := os.Create(CLOUDMETA)
	if err != nil {
		fmt.Println("create cloud-init meta file failed, File:", CLOUDMETA)
		return err
	}
	defer f_meta_data.Close()
	if _, err := f_meta_data.WriteString(meta_data_content); err != nil {
		fmt.Println("write cloud-init meta file failed, File:", CLOUDMETA)
		return err
	}

	// write user-data
	log.Println("writing the cloud-init user")
	realname, err := findAnswer(SUBIQUITY_ANSWERS, "Identity", "realname")
	if err != nil {
		fmt.Println("Finding realname error: ", err)
		return err
	}
	username, err := findAnswer(SUBIQUITY_ANSWERS, "Identity", "username")
	if err != nil {
		fmt.Println("Finding username error: ", err)
		return err
	}
	hostname, err := findAnswer(SUBIQUITY_ANSWERS, "Identity", "hostname")
	if err != nil {
		fmt.Println("Finding hostname error: ", err)
		return err
	}
	passwd, err := findAnswer(SUBIQUITY_ANSWERS, "Identity", "password")
	if err != nil {
		fmt.Println("Finding password error: ", err)
		return err
	}
	user_data_content := strings.Replace(COULD_INIT_DEFALUT_USER_DATA, "###HOSTNAME###", hostname, -1)
	user_data_content = strings.Replace(user_data_content, "###REALNAME###", realname, -1)
	user_data_content = strings.Replace(user_data_content, "###USERNAME###", username, -1)
	user_data_content = strings.Replace(user_data_content, "###PASSWDSALTED###", passwd, -1)
	f_user_data, err := os.Create(CLOUDUSER)
	if err != nil {
		fmt.Println("create cloud-init user-data file failed, File:", CLOUDUSER)
		return err
	}
	defer f_user_data.Close()
	if _, err := f_user_data.WriteString(user_data_content); err != nil {
		fmt.Println("write cloud-init user-data file failed, File:", CLOUDUSER)
		return err
	}

	// write ds-identity
	f_ds_identity, err := os.Create(CLOUD_DSIDENTITY)
	if err != nil {
		fmt.Println("create cloud-init ds-identity file failed, File:", CLOUD_DSIDENTITY)
		return err
	}
	defer f_ds_identity.Close()
	if _, err := f_ds_identity.WriteString("policy: enabled"); err != nil {
		fmt.Println("write cloud-init ds-identity file failed, File:", CLOUD_DSIDENTITY)
		return err
	}

	syscall.Unmount(CURTIN_BOOT_MNT, 0)
	syscall.Unmount(CURTIN_INSTALL_TARGET, 0)
	return nil
}

// 1. generate curtin config
// 2. call curtin
// 3. write cloud-init files
// 4. set grub
// 5. set boot entry (efibootmgr)
