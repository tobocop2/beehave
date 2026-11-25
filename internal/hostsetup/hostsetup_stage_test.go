package hostsetup_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
)

const (
	rootDone    = "HOST-SETUP-ROOT-DONE"
	jobUser     = "beehave-jobs"
	jobUserLog  = "useradd --create-home --home-dir /home/beehave-jobs --shell /bin/sh beehave-jobs\n"
	unsafeClone = "can be written by a user other than root and the admin"
)

// rootGroups are the groups that give root, by the group name that a Debian or Ubuntu host uses.
var rootGroups = []string{"root", "sudo", "admin", "wheel", "google-sudoers", "docker", "lxd", "disk", "shadow", "staff"}

// jobKeyFile is where the root stage puts the key of the controller for the job user.
func (h *fakeHost) jobKeyFile() string {
	return filepath.Join(h.sysroot, "home", jobUser, ".ssh/authorized_keys")
}

func TestHostSetupRoot_MakesTheJobUserWithNoSudoAndNoExtraGroup(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	log := h.read(h.log)
	assert.Contains(t, log, "\nsudo "+jobUserLog, "useradd makes the user: the guest agent puts only the users it makes into google-sudoers")
	assert.NotRegexp(t, `\nsudo useradd [^\n]*( -G| --groups| -g | --gid| -o | -u )`, log, "the job user gets no extra group, no shared gid and no chosen uid")
	assert.NotContains(t, log, "sudoers", "the job user gets no sudoers entry")
	assert.Equal(t, rootDone, lastLine(out))
}

func TestHostSetupRoot_PutsTheControllerKeyInTheAuthorizedKeysOfTheJobUser(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	assert.Equal(t, controllerKey, h.read(h.jobKeyFile()))

	log := h.read(h.log)
	assert.NotContains(t, log, "ssh-keys", "the key goes to the authorized_keys file only, never to the instance metadata, where the guest agent would read it and add the user to google-sudoers")
}

func TestHostSetupRoot_InstallsTheToolsTheGuardAndLingerBeforeItReportsDone(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	log := h.read(h.log)
	assert.Regexp(t, `\napt-get install [^\n]* nftables\n`, log, "the nft command that the unit runs comes from the nftables package")
	assert.Contains(t, log, "\nsudo loginctl enable-linger "+jobUser+"\n", "the job user keeps a user manager of its own")
	assert.Contains(t, log, "\nsudo systemctl restart metadata-guard.service\n")
	assert.Contains(t, out, "metadata root=200\n", "root keeps the token: the idle watchdog runs as root")
	assert.NotContains(t, log, "\nmake ", "the root stage runs no code of the clone")
	assert.NotContains(t, log, "systemd-run", "the root stage starts no job")
}

// adminGroupWrites makes a folder of the clone writable by its group, and runs the root stage as the admin builder whose primary group
// that is. It returns the gid. The case sets what the fake getent prints.
func (h *fakeHost) adminGroupWrites(t *testing.T) string {
	t.Helper()

	folder := filepath.Join(h.clone, "scripts")
	require.NoError(t, exectest.ChmodFolder(folder, 0o775))

	info, err := os.Stat(folder)
	require.NoError(t, err)

	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "the test needs the group of a folder")

	gid := strconv.FormatUint(uint64(stat.Gid), 10)
	h.env = append(h.env, "SUDO_GID="+gid, "SUDO_USER=builder")

	return gid
}

// linkToSafeFolder replaces /usr/local/<name> with a link to a new folder that only the test user can write.
func (h *fakeHost) linkToSafeFolder(t *testing.T, name string) {
	t.Helper()

	link := filepath.Join(h.sysroot, "usr/local", name)
	target := filepath.Join(h.sysroot, "srv", name)
	require.NoError(t, h.root.MkdirAll(h.rel(target), 0o750))
	require.NoError(t, h.root.RemoveAll(h.rel(link)))
	require.NoError(t, h.root.Symlink(target, h.rel(link)))
}

// adminPasswd is the passwd line of the admin builder, whose primary group is gid.
func adminPasswd(gid string) string {
	return "builder:x:" + strconv.Itoa(os.Getuid()) + ":" + gid + "::/home/builder:/bin/sh"
}

func TestHostSetupRoot_JudgesNoFolderAboveTheSystemRoot(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, exectest.ChmodFolder(h.base, 0o777))

	out, err := h.rootSetup()
	require.NoError(t, err, out)
	assert.Equal(t, rootDone, lastLine(out), "on a host the walk stops at /, so in a test it stops at the folder that stands for /")
}

func TestHostSetupRoot_AcceptsAStickyFolderAboveTheClone(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, exectest.ChmodFolder(filepath.Dir(h.clone), 0o777|os.ModeSticky))

	out, err := h.rootSetup()
	require.NoError(t, err, out)
	assert.Equal(t, rootDone, lastLine(out), "in a sticky folder such as /tmp a user cannot rename the folders of others, so the clone stays safe")
}

func TestHostSetupRoot_AcceptsAFolderThatOnlyTheGroupOfTheAdminCanWrite(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	gid := h.adminGroupWrites(t)
	h.env = append(h.env, "FAKE_GETENT_GROUP=builder:x:"+gid+":builder", "FAKE_GETENT_PASSWD=root:x:0:0::/root:/bin/sh\n"+adminPasswd(gid))

	out, err := h.rootSetup()
	require.NoError(t, err, out)
	assert.Equal(t, rootDone, lastLine(out), "a umask of 002 makes the clone of the admin writable by the private group of the admin, which holds no other user")
}

func TestHostSetupRoot_WritesTheKeyAsTheJobUser(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	copied := filepath.Join(h.sysroot, "etc/beehave/job-key.pub")
	log := h.read(h.log)
	assert.Contains(t, log, "\nsudo install -D -m 0644 "+h.jobKey+" "+copied+"\n", "root copies the key to a root folder")
	assert.Contains(t, log, "\nsudo -u beehave-jobs install -d -m 0700 "+filepath.Dir(h.jobKeyFile())+"\n")
	assert.Contains(t, log, "\nsudo -u beehave-jobs install -m 0600 "+copied+" "+h.jobKeyFile()+"\n")
	assert.NotContains(t, log, "-o beehave-jobs", "root writes nothing in the home of the job user, so a link there does not lead root")
	assert.Equal(t, controllerKey, h.read(h.jobKeyFile()))
}

func TestHostSetupRoot_RefusesBeforeAnyChange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		env     string
		message string
		prepare func(t *testing.T, h *fakeHost)
		args    []string
	}{
		{name: "a_user_that_is_not_root", env: "FAKE_UID=1000", message: "run the root stage as root"},
		{name: "an_arm_machine", env: "FAKE_ARCH=aarch64", message: "linux amd64 only"},
		{name: "a_clone_without_a_makefile", message: "holds no go.mod and Makefile", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.clone, "Makefile"))))
		}},
		{name: "a_file_of_the_clone_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Chmod(h.rel(filepath.Join(h.clone, "Makefile")), 0o664))
		}},
		{name: "a_file_of_the_clone_that_every_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Chmod(h.rel(filepath.Join(h.clone, "scripts/host-tools.sh")), 0o646))
		}},
		{name: "a_folder_of_the_clone_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, exectest.ChmodFolder(filepath.Join(h.clone, "scripts"), 0o770))
		}},
		{name: "a_folder_of_the_admin_group_that_has_another_member", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			gid := h.adminGroupWrites(t)
			h.env = append(h.env, "FAKE_GETENT_GROUP=builder:x:"+gid+":builder,alice", "FAKE_GETENT_PASSWD="+adminPasswd(gid))
		}},
		{name: "a_folder_of_the_admin_group_that_is_the_primary_group_of_another_user", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			gid := h.adminGroupWrites(t)
			h.env = append(h.env, "FAKE_GETENT_GROUP=builder:x:"+gid+":", "FAKE_GETENT_PASSWD="+adminPasswd(gid)+"\nalice:x:4321:"+gid+"::/home/alice:/bin/sh")
		}},
		{name: "a_folder_of_the_admin_group_that_getent_does_not_know", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			gid := h.adminGroupWrites(t)
			h.env = append(h.env, "FAKE_GETENT_PASSWD="+adminPasswd(gid))
		}},
		{name: "a_folder_of_the_admin_group_whose_users_getent_cannot_list", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			gid := h.adminGroupWrites(t)
			h.env = append(h.env, "FAKE_GETENT_GROUP=builder:x:"+gid+":")
		}},
		{name: "a_go_folder_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, exectest.ChmodFolder(filepath.Join(h.sysroot, "usr/local/go"), 0o775))
		}},
		{name: "a_local_bin_folder_that_every_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, exectest.ChmodFolder(filepath.Join(h.sysroot, "usr/local/bin"), 0o777))
		}},
		{name: "a_file_of_another_owner", env: "SUDO_UID=" + strconv.Itoa(os.Getuid()+1), message: unsafeClone, prepare: func(t *testing.T, _ *fakeHost) {
			t.Helper()
			if os.Getuid() == 0 {
				t.Skip("the files of the test are root files when the test runs as root")
			}
		}},
		{name: "no_job_key", message: "--job-key is required", args: []string{"--stage", "root", "--watchdog", watchdogUnit}},
		{name: "a_job_key_file_that_is_missing", message: "the job key file <base>/none does not exist", args: []string{"--stage", "root", "--watchdog", watchdogUnit, "--job-key", "<base>/none"}},
		{name: "a_private_key", message: "holds a private key", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n")
		}},
		{name: "a_parent_folder_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, exectest.ChmodFolder(filepath.Dir(h.clone), 0o775))
		}},
		{name: "a_parent_folder_of_another_owner", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			parent := filepath.Dir(h.clone)
			require.NoError(t, exectest.ChmodFolder(parent, 0o755))
			h.owns(strconv.Itoa(os.Getuid()+1), parent)
		}},
		{name: "a_parent_folder_whose_owner_stat_cannot_read", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.owns("-", filepath.Dir(h.clone))
		}},
		{name: "a_go_link_to_a_missing_folder_in_a_folder_of_another_owner", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			goRoot := filepath.Join(h.sysroot, "usr/local/go")
			srv := filepath.Join(h.sysroot, "srv")
			require.NoError(t, h.root.MkdirAll(h.rel(srv), 0o755))
			h.owns(strconv.Itoa(os.Getuid()+1), srv)
			require.NoError(t, h.root.RemoveAll(h.rel(goRoot)))
			require.NoError(t, h.root.Symlink(filepath.Join(srv, "go"), h.rel(goRoot)))
		}},
		{name: "go_and_bin_links_in_a_local_folder_every_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			// Both targets are safe. The folder that holds the two links lets every user replace them.
			h.linkToSafeFolder(t, "go")
			h.linkToSafeFolder(t, "bin")
			require.NoError(t, exectest.ChmodFolder(filepath.Join(h.sysroot, "usr/local"), 0o777))
		}},
		{name: "a_go_link_to_a_link_in_a_folder_every_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			// /usr/local/go -> srv/go -> a safe folder; only srv, the folder of the second link, lets every user replace it.
			goRoot := filepath.Join(h.sysroot, "usr/local/go")
			srv := filepath.Join(h.sysroot, "srv")
			safe := filepath.Join(h.sysroot, "opt/go")
			require.NoError(t, h.root.MkdirAll(h.rel(safe), 0o750))
			require.NoError(t, h.root.MkdirAll(h.rel(srv), 0o750))
			require.NoError(t, h.root.Symlink(safe, h.rel(filepath.Join(srv, "go"))))
			require.NoError(t, exectest.ChmodFolder(srv, 0o777))
			require.NoError(t, h.root.RemoveAll(h.rel(goRoot)))
			require.NoError(t, h.root.Symlink(filepath.Join(srv, "go"), h.rel(goRoot)))
		}},
		{name: "a_local_folder_that_is_a_link_in_a_folder_every_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			// /usr/local -> opt/local holds plain go and bin folders; only /usr, the folder of the link, lets every user replace it.
			local := filepath.Join(h.sysroot, "usr/local")
			moved := filepath.Join(h.sysroot, "opt/local")
			require.NoError(t, h.root.MkdirAll(h.rel(filepath.Dir(moved)), 0o750))
			require.NoError(t, h.root.Rename(h.rel(local), h.rel(moved)))
			require.NoError(t, h.root.Symlink(moved, h.rel(local)))
			require.NoError(t, exectest.ChmodFolder(filepath.Join(h.sysroot, "usr"), 0o777))
		}},
		{name: "a_go_link_in_a_loop_of_links", message: "a loop of links", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			goRoot := filepath.Join(h.sysroot, "usr/local/go")
			loop := filepath.Join(h.sysroot, "srv/loop")
			require.NoError(t, h.root.MkdirAll(h.rel(filepath.Dir(loop)), 0o750))
			require.NoError(t, h.root.Symlink(goRoot, h.rel(loop)))
			require.NoError(t, h.root.RemoveAll(h.rel(goRoot)))
			require.NoError(t, h.root.Symlink(loop, h.rel(goRoot)))
		}},
		{name: "a_clone_link_in_a_folder_every_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			via := filepath.Join(h.sysroot, "via")
			require.NoError(t, h.root.MkdirAll(h.rel(via), 0o750))
			require.NoError(t, h.root.Symlink(h.clone, h.rel(filepath.Join(via, "clone"))))
			require.NoError(t, exectest.ChmodFolder(via, 0o777))
			h.clone = filepath.Join(via, "clone")
		}},
		{name: "a_clone_reached_through_a_link", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			// The link sits right below the folder that stands for /, and only the folder above the target lets other users write.
			require.NoError(t, h.root.Symlink(h.clone, h.rel(filepath.Join(h.sysroot, "clone"))))
			require.NoError(t, exectest.ChmodFolder(filepath.Dir(h.clone), 0o777))
			h.clone = filepath.Join(h.sysroot, "clone")
		}},
		{name: "a_go_folder_that_links_to_a_folder_the_job_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			goRoot := filepath.Join(h.sysroot, "usr/local/go")
			target := filepath.Join(h.sysroot, "srv/go")
			require.NoError(t, h.root.MkdirAll(h.rel(target), 0o750))
			require.NoError(t, exectest.ChmodFolder(target, 0o777))
			require.NoError(t, h.root.RemoveAll(h.rel(goRoot)))
			require.NoError(t, h.root.Symlink(target, h.rel(goRoot)))
		}},
		{name: "a_system_root_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, exectest.ChmodFolder(h.sysroot, 0o775))
		}},
		{name: "two_keys", message: "more than one line", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, controllerKey+controllerKey)
		}},
		{name: "a_line_that_is_not_a_key", message: "starts with neither", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, "command=\"x\" "+controllerKey)
		}},
		{name: "an_empty_key_file", message: "holds no public key", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, "")
		}},
		{name: "a_work_root_argument", message: "--work-root is an argument of the job stage", args: []string{"--stage", "root", "--work-root", "/srv/w"}},
		{name: "no_watchdog", message: "--watchdog is required", args: []string{"--stage", "root", "--job-key", "<key>"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			if tc.prepare != nil {
				tc.prepare(t, h)
			}

			var out string
			var err error

			if tc.args != nil {
				args := make([]string, 0, len(tc.args))
				for _, arg := range tc.args {
					args = append(args, strings.ReplaceAll(strings.ReplaceAll(arg, "<base>", h.base), "<key>", h.jobKey))
				}

				out, err = h.sh([]string{"FAKE_UID=0", "SUDO_UID=" + strconv.Itoa(os.Getuid())}, filepath.Join(h.clone, "scripts/host-setup.sh"), args...)
			} else {
				out, err = h.rootSetup(tc.env)
			}

			require.Error(t, err)
			assert.Contains(t, out, strings.ReplaceAll(tc.message, "<base>", h.base))
			assert.NotContains(t, out, rootDone)
			assert.NotContains(t, h.read(h.log), "apt-get", "the refusal comes before any change")
			assert.NotContains(t, h.read(h.log), "useradd", "the refusal comes before any change")
		})
	}
}

func TestHostSetupRoot_RefusesAJobUserThatHasARootGroup(t *testing.T) {
	t.Parallel()

	for _, group := range rootGroups {
		t.Run(group, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.rootSetup("FAKE_JOB_GROUPS=beehave-jobs " + group)

			require.Error(t, err)
			assert.Contains(t, out, "the job user "+jobUser+" is in the group "+group+", which gives root. Remove it: sudo gpasswd -d "+jobUser+" "+group)
			assert.NotContains(t, out, rootDone)
		})
	}
}

func TestHostSetupRoot_ASecondRunChangesNothing(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	h.write(h.log, "")
	out, err = h.rootSetup()
	require.NoError(t, err, out)

	log := h.read(h.log)
	for _, change := range []string{"useradd", "install -d", "install -m", "enable-linger", "restart metadata-guard"} {
		assert.NotContains(t, log, change, "a second run does not repeat: "+change)
	}

	assert.Equal(t, rootDone, lastLine(out))
}

func TestHostSetupRoot_ReplacesAKeyThatDiffers(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	h.write(h.jobKeyFile(), "ssh-ed25519 AAAAstale old@host\n")
	out, err = h.rootSetup()
	require.NoError(t, err, out)

	assert.Equal(t, controllerKey, h.read(h.jobKeyFile()), "the key of the controller is the only key of the job user")
}

func TestHostSetupRoot_FailsWhenRootDoesNotGetTheTokenFromTheMetadataServer(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"000", "403", "404"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.rootSetup("FAKE_METADATA_ROOT_CODE=" + code)

			require.Error(t, err)
			assert.Contains(t, out, "root got HTTP "+code+" from "+metadataURL4+", not 200")
			assert.NotContains(t, out, rootDone)
		})
	}
}

func TestHostSetupRoot_FailsWhenTheMetadataGuardIsNotInPlace(t *testing.T) {
	t.Parallel()

	cases := []struct{ env, message string }{
		{env: "FAKE_GUARD_NO_START=1", message: "metadata-guard.service is not active"},
		{env: "FAKE_GUARD_NO_ENABLE=1", message: "is not enabled, so a reboot would drop the rules"},
		{env: "FAKE_NFT_EMPTY=1", message: "the metadata guard rules are not loaded"},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.rootSetup(tc.env)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, out, rootDone)
		})
	}
}

func TestHostSetupJob_UsesNoSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup()
	require.NoError(t, err, out)

	assert.Empty(t, rootActions(h.read(h.log)), "a step that runs candidate code holds no root")
	assert.NotContains(t, h.read(h.log), "apt-get", "the root stage installs the packages")
	assert.NotContains(t, h.read(h.log), "sudo curl", "root is not the job stage's probe")
}

func TestHostSetupJob_FailsWithoutTheGoOfTheRootStage(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.sysroot, "usr/local/go/bin/go"))))

	out, err := h.setup()

	require.Error(t, err)
	assert.Contains(t, out, "there is no Go in "+filepath.Join(h.sysroot, "usr/local/go")+". Run the root stage")
	assert.NotContains(t, out, done)
}

func TestHostSetupJob_FailsWhenTheRootStageDidNotTurnLingerOn(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.state, "linger-builder"))))

	out, err := h.setup()

	require.Error(t, err)
	assert.Contains(t, out, "linger is off for builder. Run the root stage first")
	assert.Empty(t, rootActions(h.read(h.log)), "the job stage does not turn linger on: it has no sudo")
}

func TestHostSetupJob_RefusesAUserThatCanUseSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_SUDO_RC=0")

	require.Error(t, err)
	assert.Contains(t, out, "the user "+"builder"+" can run sudo without a password, so a step of a gate can become root and take the token. Run the job stage as the job user "+jobUser)
	assert.NotContains(t, out, done)
	assert.NotContains(t, h.read(h.log), "make ", "the refusal comes before the code of the clone runs")
}

func TestHostSetupJob_RefusesRoot(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_UID=0", "FAKE_SUDO_RC=0")

	require.Error(t, err)
	assert.Contains(t, out, "run the job stage as the job user "+jobUser+", not as root")
	assert.NotContains(t, out, done)
	assert.NotContains(t, h.read(h.log), "sudo ", "the refusal comes before any other check")
	assert.NotContains(t, h.read(h.log), "make ", "the refusal comes before the code of the clone runs")
}

func TestHostSetupJob_RefusesAUserThatCanUseSudoForSomeCommandsOnly(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_SUDO_PARTIAL=1")

	require.Error(t, err)
	assert.Contains(t, out, "the user builder can run sudo without a password")
	assert.NotContains(t, out, done)
	assert.NotContains(t, h.read(h.log), "make ", "the refusal comes before the code of the clone runs")
}

func TestHostSetupJob_ChecksAgainAtTheEndThatTheUserHasNoSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_GRANT_SUDO=1")

	require.Error(t, err)
	assert.Contains(t, out, "the user builder can run sudo without a password")
	assert.Contains(t, h.read(h.log), "make setup", "the first check passed: the grant came during the run")
	assert.NotContains(t, out, done)
	assert.NoFileExists(t, filepath.Join(h.workRoot, ".beehave/host-setup.record"), "a record that says sudo=none is never written for a user that has sudo")
}

func TestHostSetupJob_RefusesAUserInARootGroup(t *testing.T) {
	t.Parallel()

	for _, group := range rootGroups {
		t.Run(group, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.setup("FAKE_GROUPS=builder " + group)

			require.Error(t, err)
			assert.Contains(t, out, "the user builder is in the group "+group+", which gives root")
			assert.NotContains(t, out, done)
			assert.NotContains(t, h.read(h.log), "make ", "the refusal comes before the code of the clone runs")
		})
	}
}

func TestHostSetupJob_FailsWhenTheJobUserGetsAnAnswerOnAnyMetadataPort(t *testing.T) {
	t.Parallel()

	const (
		tls4 = "https://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"
		tls6 = "https://[fd20:ce::254]/computeMetadata/v1/instance/service-accounts/default/token"
	)

	cases := []struct{ env, message string }{
		{env: "FAKE_METADATA_USER_CODE=200 FAKE_METADATA_USER_RC=0", message: "the job user got HTTP 200 from " + metadataURL4},
		{env: "FAKE_METADATA_USER6_CODE=403 FAKE_METADATA_USER6_RC=0", message: "the job user got HTTP 403 from " + metadataURL6},
		{env: "FAKE_METADATA_TLS_CODE=200 FAKE_METADATA_TLS_RC=0", message: "the job user got HTTP 200 from " + tls4},
		{env: "FAKE_METADATA_TLS6_CODE=000 FAKE_METADATA_TLS6_RC=35", message: "the check of " + tls6 + " ended with curl exit code 35, which proves nothing"},
		{env: "FAKE_METADATA_USER_RC=6", message: "ended with curl exit code 6, which proves nothing"},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.setup(strings.Fields(tc.env)...)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, out, done)
		})
	}
}

func TestHostSetupJob_RecordsTheRefusalAndTheMissingSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_METADATA_USER_RC=28")

	require.NoError(t, err, out)
	assert.Contains(t, h.record(), "metadata user=refused sudo=none\n", "a timeout is no answer")
}

func TestHostSetup_RefusesAStageThatIsNotNamed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{name: "no_stage", args: []string{"--watchdog", watchdogUnit}, message: "--stage is required"},
		{name: "an_unknown_stage", args: []string{"--stage", "both"}, message: "the stage both is not root or job"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.sh(nil, filepath.Join(h.clone, "scripts/host-setup.sh"), tc.args...)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.Empty(t, strings.TrimSpace(h.read(h.log)), "the refusal comes before any call")
		})
	}
}

func TestHostTools_RefusesAStageThatIsNotNamed(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"both"}} {
		h := newFakeHost(t)
		out, err := h.sh(nil, filepath.Join(h.clone, "scripts/host-tools.sh"), args...)

		require.Error(t, err)
		assert.Contains(t, out, "name the stage: sh scripts/host-tools.sh system")
		assert.NotContains(t, h.read(h.log), "apt-get", "the refusal comes before any change")
	}
}
