package gate

import "github.com/tobocop2/beehave/internal/model"

// Tools are the absolute paths of the programs that the job of a gate program uses before head code runs: git, env, which starts git with
// the environment of the job only, and the tools of the final compare.
// The engine resolves them once, at its start, so a program that an agent step puts first on the PATH later is never used.
type Tools struct {
	Git, Env, Find, Sort, Readlink, Cat, Digest string
}

// words are the words that set the tool variables of the job to these paths. Tools that the engine did not resolve give the words that ask the PATH of the job.
func (t Tools) words() string {
	if t == (Tools{}) {
		return `gpbin=$(command -v git) && gpenv=$(command -v env) && gpfind=$(command -v find) && gpsort=$(command -v sort) && gpreadlink=$(command -v readlink) && ` +
			`gpcat=$(command -v cat) && gpshasum=$(command -v sha256sum || command -v shasum) && `
	}

	return "gpbin=" + model.ShellQuote(t.Git) + " && gpenv=" + model.ShellQuote(t.Env) + " && gpfind=" + model.ShellQuote(t.Find) + " && gpsort=" + model.ShellQuote(t.Sort) +
		" && gpreadlink=" + model.ShellQuote(t.Readlink) + " && gpcat=" + model.ShellQuote(t.Cat) + " && gpshasum=" + model.ShellQuote(t.Digest) + " && "
}
