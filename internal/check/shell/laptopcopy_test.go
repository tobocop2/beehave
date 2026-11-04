package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/check/shell"
	"github.com/tobocop2/beehave/internal/model"
)

var laptopCopyOnly = []model.FindingKind{model.FindingLaptopCopy}

func TestScanWith_ACommandThatCopiesFromThisMachineToAnotherIsRefusedForARepositoryThatIsNotPrivate(t *testing.T) {
	t.Parallel()

	commands := map[string]string{
		"rsync to host:path":          "rsync -a ./src host:/work/src",
		"rsync to user@host:path":     "rsync -az src/ me@build:/work/src",
		"rsync to a daemon":           "rsync -a src rsync://build/module/src",
		"rsync with an option value":  "rsync -e ssh src host:dst",
		"scp to host:path":            "scp file.txt host:/work/",
		"scp to user@host:path":       "scp -r src me@host:/work",
		"scp with an option value":    "scp -P 2222 -i key.pem a.txt me@host:",
		"scp to a url":                "scp a.txt ssh://me@host/work/a.txt",
		"scp to a variable host":      "scp a.txt $HOST:/work",
		"scp to an address":           "scp a.txt [::1]:/work",
		"gcloud compute scp":          "gcloud compute scp --zone z a.txt vm:/work",
		"after sudo":                  "sudo rsync -a src host:dst",
		"by path":                     "/usr/bin/scp a.txt host:",
		"in a chain":                  "cd /tmp && scp a.txt host:",
		"in a shell":                  "sh -c 'rsync -a src host:dst'",
		"after the options end":       "scp -- -odd host:",
		"a remote and a local source": "scp host:a b host2:c",
		"tar into ssh":                "tar cf - src | ssh host 'tar xf - -C /work'",
		"cat into ssh":                "cat key.pem | ssh host 'cat > key.pem'",
		"tar into ssh through a pipe": "tar cf - src | gzip | ssh me@host tar xzf -",
		"tar into gcloud ssh":         "tar cf - src | gcloud compute ssh vm --command='tar xf -'",
		"tar into a wrapper":          "tar cf - src | ./ssh-host.sh 'tar xf -'",

		"a file read into ssh":             "ssh host < key.pem",
		"a file read into ssh with a tool": "ssh host 'cat > key.pem' < key.pem",
		"a file read with ssh options":     "ssh -p 22 me@host 'cat > f' < ./f",
		"a file read into gcloud ssh":      "gcloud compute ssh vm --command='cat > f' < f",
		"a file read into a wrapper":       "./ssh-host.sh 'cat > f' < f",
		"a file read after a heredoc":      "ssh host 'cat > f' < f <<'EOF'\nls\nEOF",
		"a file read into az ssh":          "az ssh vm < f",
		"a pipe into az ssh":               "cat f | az ssh vm",
		"a pipe into a session":            "cat f | aws ssm start-session --target i-1",
		"a file read into a session":       "aws ssm start-session --target i-1 < f",
		"a pipe into kubectl exec":         "cat f | kubectl exec -i p -- tee /f",
		"a pipe into kubectl exec -it":     "tar cf - src | kubectl -n ns exec -it p -- tar xf -",
		"a pipe into kubectl exec stdin":   "cat f | kubectl exec --stdin p -- tee /f",
		"a file read into kubectl exec":    "kubectl exec -i p -- tee /f < f",
		"kubectl cp to a pod":              "kubectl cp ./a p:/b",
		"kubectl cp to a pod in a space":   "kubectl cp -n ns -c app a.txt ns/p:/b",
	}

	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			findings := shell.ScanWith(command, laptopCopyOnly, shell.Protection{})

			require.True(t, hasKind(findings, "laptop-copy"), "%v", findings)
			assert.Contains(t, findings[0].Text, "Clone the repository from the forge on the host")
		})
	}
}

func TestScanWith_ACopyThatSendsNoLaptopFileToAnotherMachinePasses(t *testing.T) {
	t.Parallel()

	commands := map[string]string{
		"scp from a host":            "scp host:/work/out.txt .",
		"scp from a user@host":       "scp -r me@host:/work/out ./out",
		"rsync from a host":          "rsync -a host:/work/out ./out",
		"rsync from a daemon":        "rsync -a rsync://build/module/out ./out",
		"scp between hosts":          "scp host:a host:b",
		"scp a local copy":           "scp a.txt b.txt",
		"rsync a local copy":         "rsync -a src/ dst/",
		"a local path with a colon":  "rsync -a ./a:b dst/",
		"an absolute path":           "cp /tmp/a:b /tmp/c",
		"scp with no operand":        "scp",
		"scp options only":           "scp -q",
		"clone on the host":          "ssh host 'git clone https://example.com/o/r.git tree'",
		"ssh with no input":          "ssh host make check",
		"ssh with a redirect":        "ssh host 'cat /work/log' > log.txt",
		"a pipe out of ssh":          "ssh host 'tar cf - /work/out' | tar xf -",
		"a copy on the host":         "ssh host 'rsync -a /work/a other:/work/a'",
		"a pipe into a local tool":   "tar cf - src | gzip > src.tgz",
		"gcloud compute scp from vm": "gcloud compute scp vm:/work/out.txt .",
		"gcloud compute ssh":         "gcloud compute ssh vm --command='git clone u tree'",
		"gcloud with no scp":         "gcloud compute instances list",
		"text that names rsync":      "echo rsync a host:b",
		"git clone over ssh":         "git clone ssh://git@host/o/r.git tree",
		"git clone scp form":         "git clone git@host:o/r.git tree",

		"a heredoc of commands into ssh":    "ssh host <<'EOF'\nmake check\nEOF",
		"a here-string into ssh":            "ssh host <<< 'make check'",
		"ssh from the null device":          "ssh host make check < /dev/null",
		"an output redirect on ssh":         "ssh host 'cat /f' > out.txt",
		"a redirect into a local tool":      "sort < f > g",
		"kubectl exec with no input":        "kubectl exec p -- ls /",
		"kubectl exec with a heredoc":       "kubectl exec -i p -- sh <<'EOF'\nls\nEOF",
		"kubectl cp from a pod":             "kubectl cp p:/b ./a",
		"kubectl cp from a pod in a space":  "kubectl cp ns/p:/b ./a",
		"kubectl cp between pods":           "kubectl cp p:/a q:/b",
		"kubectl cp a local copy":           "kubectl cp ./a:b ./c",
		"kubectl get":                       "kubectl get pods",
		"az without ssh":                    "az vm list",
		"aws ssm without a session":         "aws ssm describe-instance-information",
		"docker exec is a local daemon":     "cat f | docker exec -i c tee /f",
		"a bucket copy is the allowed way":  "gsutil cp ./a gs://bucket/a",
		"a storage copy is the allowed way": "gcloud storage cp ./a gs://bucket/a",
		"an s3 copy is the allowed way":     "aws s3 cp ./a s3://bucket/a",
		"an rclone copy is the allowed way": "rclone copy ./a remote:bucket/a",
	}

	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			findings := shell.ScanWith(command, laptopCopyOnly, shell.Protection{})

			assert.Empty(t, findings, command)
		})
	}
}

func TestScanWith_ACopyToAnotherMachinePassesForAPrivateRepositoryAndOnlyWhenTheKindIsListed(t *testing.T) {
	t.Parallel()

	const command = "rsync -a src host:dst"

	assert.Empty(t, shell.ScanWith(command, laptopCopyOnly, shell.Protection{Private: true}))
	assert.Empty(t, shell.ScanWith(command, []model.FindingKind{model.FindingOpaque}, shell.Protection{}))
	assert.NotEmpty(t, shell.ScanWith(command, laptopCopyOnly, shell.Protection{Private: false}))
}

func TestScanWith_APrivateRepositoryDoesNotTurnOffTheOtherKinds(t *testing.T) {
	t.Parallel()

	kinds := []model.FindingKind{model.FindingLaptopCopy, model.FindingBulkStaging}

	findings := shell.ScanWith("git add -A && scp a host:", kinds, shell.Protection{Private: true})

	assert.True(t, hasKind(findings, "bulk-staging"))
	assert.False(t, hasKind(findings, "laptop-copy"))
}
