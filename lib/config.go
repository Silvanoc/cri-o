package lib

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/pkg/sysregistries"
	"github.com/containers/image/pkg/sysregistriesv2"
	"github.com/containers/image/types"
	"github.com/containers/libpod/pkg/spec"
	"github.com/containers/storage"
	units "github.com/docker/go-units"
	"github.com/kubernetes-sigs/cri-o/oci"
	"github.com/sirupsen/logrus"
)

// Defaults if none are specified
const (
	pauseImage          = "k8s.gcr.io/pause:3.1"
	pauseCommand        = "/pause"
	defaultTransport    = "docker://"
	apparmorProfileName = "crio-default"
	cgroupManager       = oci.CgroupfsCgroupsManager
)

// Config represents the entire set of configuration values that can be set for
// the server. This is intended to be loaded from a toml-encoded config file.
type Config struct {
	RootConfig
	RuntimeConfig
	ImageConfig
	NetworkConfig
}

// ImageVolumesType describes image volume handling strategies
type ImageVolumesType string

const (
	// ImageVolumesMkdir option is for using mkdir to handle image volumes
	ImageVolumesMkdir ImageVolumesType = "mkdir"
	// ImageVolumesIgnore option is for ignoring image volumes altogether
	ImageVolumesIgnore ImageVolumesType = "ignore"
	// ImageVolumesBind option is for using bind mounted volumes
	ImageVolumesBind ImageVolumesType = "bind"
)

const (
	// DefaultPidsLimit is the default value for maximum number of processes
	// allowed inside a container
	DefaultPidsLimit = 1024

	// DefaultLogSizeMax is the default value for the maximum log size
	// allowed for a container. Negative values mean that no limit is imposed.
	DefaultLogSizeMax = -1
)

// DefaultCapabilities for the default_capabilities option in the crio.conf file
var DefaultCapabilities = []string{
	"CHOWN",
	"DAC_OVERRIDE",
	"FSETID",
	"FOWNER",
	"NET_RAW",
	"SETGID",
	"SETUID",
	"SETPCAP",
	"NET_BIND_SERVICE",
	"SYS_CHROOT",
	"KILL",
}

// This structure is necessary to fake the TOML tables when parsing,
// while also not requiring a bunch of layered structs for no good
// reason.

// RootConfig represents the root of the "crio" TOML config table.
type RootConfig struct {
	// Root is a path to the "root directory" where data not
	// explicitly handled by other options will be stored.
	Root string `toml:"root"`

	// RunRoot is a path to the "run directory" where state information not
	// explicitly handled by other options will be stored.
	RunRoot string `toml:"runroot"`

	// Storage is the name of the storage driver which handles actually
	// storing the contents of containers.
	Storage string `toml:"storage_driver"`

	// StorageOption is a list of storage driver specific options.
	StorageOptions []string `toml:"storage_option"`

	// LogDir is the default log directory were all logs will go unless kubelet
	// tells us to put them somewhere else.
	LogDir string `toml:"log_dir"`

	// FileLocking specifies whether to use file-based or in-memory locking
	// File-based locking is required when multiple users of lib are
	// present on the same system
	FileLocking bool `toml:"file_locking"`

	// FileLockingPath specifies the path to use for the locking.
	FileLockingPath string `toml:"file_locking_path"`
}

// RuntimeConfig represents the "crio.runtime" TOML config table.
type RuntimeConfig struct {
	// Runtime is the OCI compatible runtime used for trusted container workloads.
	// This is a mandatory setting as this runtime will be the default one and
	// will also be used for untrusted container workloads if
	// RuntimeUntrustedWorkload is not set.
	//
	// DEPRECATED: use Runtimes instead.
	//
	Runtime string `toml:"runtime"`

	// DefaultRuntime is the _name_ of the OCI runtime to be used as the default.
	// The name is matched against the Runtimes map below.
	DefaultRuntime string `toml:"default_runtime"`

	// RuntimeUntrustedWorkload is the OCI compatible runtime used for
	// untrusted container workloads. This is an optional setting, except
	// if DefaultWorkloadTrust is set to "untrusted".
	// DEPRECATED: use Runtimes instead. If provided, this runtime is
	//     mapped to the runtime handler named 'untrusted'. It is a
	//     configuration error to provide both the (now deprecated)
	//     RuntimeUntrustedWorkload and a handler in the Runtimes handler
	//     map (below) for 'untrusted' workloads at the same time. Please
	//     provide one or the other.
	//     The support of this option will continue through versions 1.12 and 1.13.
	//     By version 1.14, this option will no longer exist.
	RuntimeUntrustedWorkload string `toml:"runtime_untrusted_workload"`

	// DefaultWorkloadTrust is the default level of trust crio puts in container
	// workloads. This can either be "trusted" or "untrusted" and the default
	// is "trusted"
	// Containers can be run through different container runtimes, depending on
	// the trust hints we receive from kubelet:
	// - If kubelet tags a container workload as untrusted, crio will try first
	// to run it through the untrusted container workload runtime. If it is not
	// set, crio will use the trusted runtime.
	// - If kubelet does not provide any information about the container workload trust
	// level, the selected runtime will depend on the DefaultWorkloadTrust setting.
	// If it is set to "untrusted", then all containers except for the host privileged
	// ones, will be run by the RuntimeUntrustedWorkload runtime. Host privileged
	// containers are by definition trusted and will always use the trusted container
	// runtime. If DefaultWorkloadTrust is set to "trusted", crio will use the trusted
	// container runtime for all containers.
	// DEPRECATED: The runtime handler should provide a key to the map of runtimes,
	//     avoiding the need to rely on the level of trust of the workload to choose
	//     an appropriate runtime.
	//     The support of this option will continue through versions 1.12 and 1.13.
	//     By version 1.14, this option will no longer exist.
	DefaultWorkloadTrust string `toml:"default_workload_trust"`

	// Runtimes defines a list of OCI compatible runtimes. The runtime to
	// use is picked based on the runtime_handler provided by the CRI. If
	// no runtime_handler is provided, the runtime will be picked based on
	// the level of trust of the workload.
	Runtimes map[string]oci.RuntimeHandler `toml:"runtimes"`

	// NoPivot instructs the runtime to not use `pivot_root`, but instead use `MS_MOVE`
	NoPivot bool `toml:"no_pivot"`

	// Conmon is the path to conmon binary, used for managing the runtime.
	Conmon string `toml:"conmon"`

	// ConmonEnv is the environment variable list for conmon process.
	ConmonEnv []string `toml:"conmon_env"`

	// SELinux determines whether or not SELinux is used for pod separation.
	SELinux bool `toml:"selinux"`

	// SeccompProfile is the seccomp json profile path which is used as the
	// default for the runtime.
	SeccompProfile string `toml:"seccomp_profile"`

	// ApparmorProfile is the apparmor profile name which is used as the
	// default for the runtime.
	ApparmorProfile string `toml:"apparmor_profile"`

	// CgroupManager is the manager implementation name which is used to
	// handle cgroups for containers.
	CgroupManager string `toml:"cgroup_manager"`

	// HooksDir holds paths to the directories containing hooks
	// configuration files.  When the same filename is present in in
	// multiple directories, the file in the directory listed last in
	// this slice takes precedence.
	HooksDir []string `toml:"hooks_dir"`

	// DefaultMounts is the list of mounts to be mounted for each container
	// The format of each mount is "host-path:container-path"
	DefaultMounts []string `toml:"default_mounts"`

	// DefaultMountsFile is the file path for the default mounts to be mounted for the container
	// Note, for testing purposes mainly
	DefaultMountsFile string `toml:"default_mounts_file"`

	// PidsLimit is the number of processes each container is restricted to
	// by the cgroup process number controller.
	PidsLimit int64 `toml:"pids_limit"`

	// LogSizeMax is the maximum number of bytes after which the log file
	// will be truncated. It can be expressed as a human-friendly string
	// that is parsed to bytes.
	// Negative values indicate that the log file won't be truncated.
	LogSizeMax int64 `toml:"log_size_max"`

	// ContainerExitsDir is the directory in which container exit files are
	// written to by conmon.
	ContainerExitsDir string `toml:"container_exits_dir"`

	// ContainerAttachSocketDir is the location for container attach sockets.
	ContainerAttachSocketDir string `toml:"container_attach_socket_dir"`

	// ManageNetworkNSLifecycle determines whether we pin and remove network namespace
	// and manage its lifecycle
	ManageNetworkNSLifecycle bool `toml:"manage_network_ns_lifecycle"`

	// ReadOnly run all pods/containers in read-only mode.
	// This mode will mount tmpfs on /run, /tmp and /var/tmp, if those are not mountpoints
	// Will also set the readonly flag in the OCI Runtime Spec.  In this mode containers
	// will only be able to write to volumes mounted into them
	ReadOnly bool `toml:"read_only"`

	// BindMountPrefix is the prefix to use for the source of the bind mounts.
	BindMountPrefix string `toml:"bind_mount_prefix"`

	// UIDMappings specifies the UID mappings to have in the user namespace.
	// A range is specified in the form containerUID:HostUID:Size.  Multiple
	// ranges are separated by comma.
	UIDMappings string `toml:"uid_mappings"`

	// GIDMappings specifies the GID mappings to have in the user namespace.
	// A range is specified in the form containerUID:HostUID:Size.  Multiple
	// ranges are separated by comma.
	GIDMappings string `toml:"gid_mappings"`

	// Capabilities to add to all containers.
	DefaultCapabilities []string `toml:"default_capabilities"`

	// LogLevel determines the verbosity of the logs based on the level it is set to.
	// Options are fatal, panic, error (default), warn, info, and debug.
	LogLevel string `toml:"log_level"`

	// CtrStopTimeout specifies the time to wait before to generate an
	// error because the container state is still tagged as "running".
	CtrStopTimeout int64 `toml:"ctr_stop_timeout"`

	// Sysctls to add to all containers.
	DefaultSysctls []string `toml:"default_sysctls"`

	// DefeultUlimits specifies the default ulimits to apply to containers
	DefaultUlimits []string `toml:"default_ulimits"`

	// Devices to add to containers
	AdditionalDevices []string `toml:"additional_devices"`
}

// ImageConfig represents the "crio.image" TOML config table.
type ImageConfig struct {
	// DefaultTransport is a value we prefix to image names that fail to
	// validate source references.
	DefaultTransport string `toml:"default_transport"`
	// PauseImage is the name of an image which we use to instantiate infra
	// containers.
	PauseImage string `toml:"pause_image"`
	// PauseCommand is the path of the binary we run in an infra
	// container that's been instantiated using PauseImage.
	PauseCommand string `toml:"pause_command"`
	// SignaturePolicyPath is the name of the file which decides what sort
	// of policy we use when deciding whether or not to trust an image that
	// we've pulled.  Outside of testing situations, it is strongly advised
	// that this be left unspecified so that the default system-wide policy
	// will be used.
	SignaturePolicyPath string `toml:"signature_policy"`
	// InsecureRegistries is a list of registries that must be contacted w/o
	// TLS verification.
	InsecureRegistries []string `toml:"insecure_registries"`
	// ImageVolumes controls how volumes specified in image config are handled
	ImageVolumes ImageVolumesType `toml:"image_volumes"`
	// Registries holds a list of registries used to pull unqualified images
	Registries []string `toml:"registries"`
}

// NetworkConfig represents the "crio.network" TOML config table
type NetworkConfig struct {
	// NetworkDir is where CNI network configuration files are stored.
	NetworkDir string `toml:"network_dir"`

	// PluginDir is where CNI plugin binaries are stored.
	PluginDir string `toml:"plugin_dir"`
}

// tomlConfig is another way of looking at a Config, which is
// TOML-friendly (it has all of the explicit tables). It's just used for
// conversions.
type tomlConfig struct {
	Crio struct {
		RootConfig
		Runtime struct{ RuntimeConfig } `toml:"runtime"`
		Image   struct{ ImageConfig }   `toml:"image"`
		Network struct{ NetworkConfig } `toml:"network"`
	} `toml:"crio"`
}

func (t *tomlConfig) toConfig(c *Config) {
	c.RootConfig = t.Crio.RootConfig
	c.RuntimeConfig = t.Crio.Runtime.RuntimeConfig
	c.ImageConfig = t.Crio.Image.ImageConfig
	c.NetworkConfig = t.Crio.Network.NetworkConfig
}

func (t *tomlConfig) fromConfig(c *Config) {
	t.Crio.RootConfig = c.RootConfig
	t.Crio.Runtime.RuntimeConfig = c.RuntimeConfig
	t.Crio.Image.ImageConfig = c.ImageConfig
	t.Crio.Network.NetworkConfig = c.NetworkConfig
}

// UpdateFromFile populates the Config from the TOML-encoded file at the given path.
// Returns errors encountered when reading or parsing the files, or nil
// otherwise.
func (c *Config) UpdateFromFile(path string) error {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	t := new(tomlConfig)
	t.fromConfig(c)

	_, err = toml.Decode(string(data), t)
	if err != nil {
		return err
	}

	t.toConfig(c)
	return nil
}

// ToFile outputs the given Config as a TOML-encoded file at the given path.
// Returns errors encountered when generating or writing the file, or nil
// otherwise.
func (c *Config) ToFile(path string) error {
	var w bytes.Buffer
	e := toml.NewEncoder(&w)

	t := new(tomlConfig)
	t.fromConfig(c)

	if err := e.Encode(*t); err != nil {
		return err
	}

	return ioutil.WriteFile(path, w.Bytes(), 0644)
}

// DefaultConfig returns the default configuration for crio.
func DefaultConfig() *Config {
	registries, _ := sysregistries.GetRegistries(&types.SystemContext{})
	insecureRegistries, _ := sysregistries.GetInsecureRegistries(&types.SystemContext{})
	return &Config{
		RootConfig: RootConfig{
			Root:            storage.DefaultStoreOptions.GraphRoot,
			RunRoot:         storage.DefaultStoreOptions.RunRoot,
			Storage:         storage.DefaultStoreOptions.GraphDriverName,
			StorageOptions:  storage.DefaultStoreOptions.GraphDriverOptions,
			LogDir:          "/var/log/crio/pods",
			FileLocking:     true,
			FileLockingPath: lockPath,
		},
		RuntimeConfig: RuntimeConfig{
			DefaultRuntime: "runc",
			Runtimes: map[string]oci.RuntimeHandler{
				"runc": {
					RuntimePath: "/usr/bin/runc",
				},
			},
			Conmon: conmonPath,
			ConmonEnv: []string{
				"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			},
			SELinux:                  selinuxEnabled(),
			SeccompProfile:           seccompProfilePath,
			ApparmorProfile:          apparmorProfileName,
			CgroupManager:            cgroupManager,
			PidsLimit:                DefaultPidsLimit,
			ContainerExitsDir:        containerExitsDir,
			ContainerAttachSocketDir: oci.ContainerAttachSocketDir,
			LogSizeMax:               DefaultLogSizeMax,
			DefaultCapabilities:      DefaultCapabilities,
			LogLevel:                 "error",
			DefaultSysctls:           []string{},
			DefaultUlimits:           []string{},
			AdditionalDevices:        []string{},
		},
		ImageConfig: ImageConfig{
			DefaultTransport:    defaultTransport,
			PauseImage:          pauseImage,
			PauseCommand:        pauseCommand,
			SignaturePolicyPath: "",
			ImageVolumes:        ImageVolumesMkdir,
			Registries:          registries,
			InsecureRegistries:  insecureRegistries,
		},
		NetworkConfig: NetworkConfig{
			NetworkDir: cniConfigDir,
			PluginDir:  cniBinDir,
		},
	}
}

// Validate is the main entry point for runtime configuration validation
// The parameter `onExecution` specifies if the validation should include
// execution checks. It returns an `error` on validation failure, otherwise
// `nil`.
func (c *RuntimeConfig) Validate(onExecution bool) error {
	// This is somehow duplicated with server.getUlimitsFromConfig under server/utils.go
	// but I don't want to export that function for the sake of validation here
	// so, keep it in mind if things start to blow up.
	// Reason for having this here is that I don't want people to start crio
	// with invalid ulimits but realize that only after starting a couple of
	// containers and watching them fail.
	for _, u := range c.DefaultUlimits {
		ul, err := units.ParseUlimit(u)
		if err != nil {
			return fmt.Errorf("unrecognized ulimit %s: %v", u, err)
		}
		_, err = ul.GetRlimit()
		if err != nil {
			return err
		}
	}

	for _, d := range c.AdditionalDevices {
		split := strings.Split(d, ":")
		switch len(split) {
		case 3:
			if !createconfig.IsValidDeviceMode(split[2]) {
				return fmt.Errorf("invalid device mode: %s", split[2])
			}
			fallthrough
		case 2:
			if (!createconfig.IsValidDeviceMode(split[1]) && !strings.HasPrefix(split[1], "/dev/")) ||
				(len(split) == 3 && createconfig.IsValidDeviceMode(split[1])) {
				return fmt.Errorf("invalid device mode: %s", split[1])
			}
			fallthrough
		case 1:
			if !strings.HasPrefix(split[0], "/dev/") {
				return fmt.Errorf("invalid device mode: %s", split[0])
			}
		default:
			return fmt.Errorf("invalid device specification: %s", d)
		}
	}

	// check we do have at least a runtime
	_, ok := c.Runtimes[c.DefaultRuntime]
	if !ok && c.Runtime == "" && c.RuntimeUntrustedWorkload == "" {
		return errors.New("no default runtime configured")
	}

	const deprecatedRuntimeMsg = `
/*\ Warning /*\
DEPRECATED: Use Runtimes instead.

The support of this option will continue through versions 1.12 and 1.13. By
version 1.14, this option will no longer exist.
/*\ Warning /*\
`

	// check for deprecated options
	if c.RuntimeUntrustedWorkload != "" {
		logrus.Warnf("RuntimeUntrustedWorkload deprecated\n%s", deprecatedRuntimeMsg)
	}
	if c.DefaultWorkloadTrust != "" {
		logrus.Warnf("DefaultWorkloadTrust deprecated\n%s", deprecatedRuntimeMsg)
	}
	if c.Runtime != "" {
		logrus.Warn("runtime is deprecated in favor of runtimes, please switch to that")
	}

	// check for conflicting options
	_, ok = c.Runtimes[oci.UntrustedRuntime]
	if ok && c.RuntimeUntrustedWorkload != "" {
		return fmt.Errorf(
			"conflicting definitions: configuration includes runtime_untrusted_workload and runtimes[%q]",
			oci.UntrustedRuntime,
		)
	}

	// check for validation on execution
	if onExecution {
		if c.Runtime != "" {
			if _, err := os.Stat(c.Runtime); os.IsNotExist(err) {
				// path to runtime does not exist
				return fmt.Errorf("invalid --runtime value %q", err)
			}
		}

		// Validate if runtime_path does exist for each runtime
		for runtime, handler := range c.Runtimes {
			if _, err := os.Stat(handler.RuntimePath); os.IsNotExist(err) {
				return fmt.Errorf("invalid runtime_path for runtime '%s': %q",
					runtime, err)
			}
			logrus.Debugf("found valid runtime '%s' for runtime_path '%s'\n",
				runtime, handler.RuntimePath)
		}

		// Validate the system registries configuration
		if _, err := sysregistriesv2.GetRegistries(nil); err != nil {
			return fmt.Errorf("invalid /etc/containers/registries.conf: %q", err)
		}
	}

	return nil
}
