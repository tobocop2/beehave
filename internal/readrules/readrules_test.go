// Package readrules holds one test: every interface of the engine that returns a value with an error is classified, and a read that decides something has a failed-read case.
package readrules_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roots are the trees that the test walks. It finds every interface by itself, so a new package or a new interface needs no edit here.
var roots = []string{"internal", "cmd"}

// rule says what holds for one method. There is no entry for a whole interface: a new method on any interface fails the test.
// Proof names a test in the package of the interface that injects a failure for the method and asserts the refusal; Needle is the name of the injection in that test:
// a string literal that holds it, a key of a composite literal, or the field that an assignment sets. A word in a comment or the name of a helper does not count.
// Exempt gives the reason why the method is no read that decides a request. NotHeld says that the failed-read case is not proven by this test: the row lists those places as not held.
type rule struct {
	Proof   string
	Needle  string
	Exempt  string
	NotHeld string
}

const (
	mergeProof = "TestRun_AReadThatFailsRefusesTheMerge"
	batchProof = "TestProve_AReadThatFailsLeavesTheMemberUnprovenNeverLanded"
	inAdapter  = "the adapter or the consumer has its own failure tests, and this test does not prove them"
	noEngine   = "a read for display or for a command line, which this test does not prove"
	doesWork   = "the method starts, creates or records something; its error is the result of the action"
)

// rules maps "directory.Interface.Method" to its rule.
// A method with a value and an error that is in no rule fails TestEveryReadOfEveryInterfaceIsClassified.
var rules = map[string]rule{
	"internal/merge.Reads.PRView":             {Proof: mergeProof, Needle: "PRView"},
	"internal/merge.Reads.PRList":             {Proof: mergeProof, Needle: "PRList"},
	"internal/merge.Reads.PullCounts":         {Proof: mergeProof, Needle: "PullCounts"},
	"internal/merge.Reads.Committers":         {Proof: mergeProof, Needle: "Committers"},
	"internal/merge.Reads.PRChecks":           {Proof: mergeProof, Needle: "PRChecks"},
	"internal/merge.Reads.OpenChangeRequests": {Proof: mergeProof, Needle: "OpenChangeRequests"},
	"internal/merge.Reads.UnresolvedThreads":  {Proof: mergeProof, Needle: "UnresolvedThreads"},
	"internal/merge.Reads.StackID":            {Proof: mergeProof, Needle: "StackID"},
	"internal/merge.Reads.TokenScopes":        {Proof: mergeProof, Needle: "TokenScopes"},
	"internal/merge.Reads.BranchRef":          {Proof: mergeProof, Needle: "BranchRef"},
	"internal/merge.Closings.ClosingRefs":     {Proof: mergeProof, Needle: "closing references"},
	"internal/merge.Approvals.Reviews":        {Proof: mergeProof, Needle: "Reviews"},
	"internal/merge.Ancestry.Compare":         {Proof: "TestRun_AnAncestryThatCannotBeReadFailsTheItem", Needle: "ancestryErr"},
	"internal/batch.ProofReads.Compare":       {Proof: batchProof, Needle: "compareErr"},
	"internal/batch.ProofReads.CommitTree":    {Proof: batchProof, Needle: "treeErr"},
	"internal/batch.ProofReads.TreeBlobs":     {Proof: batchProof, Needle: "blobsErr"},
	"internal/batch.ProofReads.BranchRef":     {Proof: "TestProve_ABranchThatCannotBeReadLeavesTheMemberUnproven", Needle: "branchErr"},
	"internal/flow.batchHost.Prove":           {Proof: "TestBatchMerge_AProofThatErrorsEndsTheCallWithTheError", Needle: "proofErr"},
	"internal/merge.Trees.CommitTree":         {Proof: "TestTree_ATipWhoseTreeCannotBeReadStops", Needle: "CommitTree"},
	"internal/merge.Trees.TreeBlobs":          {Proof: "TestTree_ADifferentTreeStillStopsWhenThePathsCannotBeListed", Needle: "listFail"},
	"internal/merge.Logs.FailedLog":           {Exempt: "the log of a failed check helps the reader; a log that cannot be read leaves the row without text and the check stays red"},
	"internal/merge.Body.PRBody":              {Proof: "TestEditBody_AFailedReadWritesNothing", Needle: "failRead"},

	"internal/preflight.Reader.PRView":    {Proof: "TestCheck_AFailedReadIsARefusalThatKeepsItsCause", Needle: "viewErr"},
	"internal/preflight.Reader.BranchRef": {Proof: "TestCheck_AFailedReadIsARefusalThatKeepsItsCause", Needle: "branchErr"},

	"internal/review.Reader.PRList":            {Proof: "TestPoll_AFailedReadStopsWithAnUnreadError", Needle: "PRList"},
	"internal/review.Reader.PRView":            {Proof: "TestPoll_AFailedReadStopsWithAnUnreadError", Needle: "PRView"},
	"internal/review.Reader.Reviews":           {Proof: "TestPoll_AFailedReadStopsWithAnUnreadError", Needle: "Reviews"},
	"internal/review.Reader.Comments":          {Proof: "TestPoll_AFailedCommentReadOnACurrentPullRequestStopsThePoll", Needle: "Comments"},
	"internal/review.Reader.ReviewComments":    {Proof: "TestReplies_AnUnreadableItemStopsThePoll", Needle: "ReviewComments"},
	"internal/review.Reader.Search":            {Proof: "TestReplies_AFailedSearchStopsThePollForEachKind", Needle: "Search"},
	"internal/review.Reader.PRChecks":          {Proof: "TestCI_ADraftIsSkippedAndAFailedReadStopsThePoll", Needle: "PRChecks"},
	"internal/review.Reader.Title":             {Exempt: "the title only labels a row; a title that cannot be read shows nothing (TestReplies_APullRequestSearchReadsTheReviewPathsAndAFailedTitleShowsNothing)"},
	"internal/review.Reader.CheckNotStarted":   {Exempt: "an annotation that cannot be read counts the check as ran, so the row stays red (TestCI_AFailedAnnotationReadCountsAsRan)"},
	"internal/review.Own.OwnPulls":             {Proof: "TestOwn_AFailedOwnReadStopsThePoll", Needle: "OwnPulls"},
	"internal/review.Stacks.StackID":           {Proof: "TestSweep_AnUnreadableStackIsFlagged", Needle: "StackID"},
	"internal/review.Stacks.DefaultBranch":     {Exempt: "the default branch only explains a base branch on a row; a branch that cannot be read shows unknown (TestSweep_ADefaultBranchThatCannotBeReadIsUnknown)"},
	"internal/review.Pulls.PRView":             {Proof: "TestWatch_AFailedReadIsAnErrorAndNeverWaiting", Needle: "PRView"},
	"internal/review.Pulls.OpenChangeRequests": {Proof: "TestWatch_AFailedReadIsAnErrorAndNeverWaiting", Needle: "OpenChangeRequests"},
	"internal/review.Pulls.UnresolvedThreads":  {Proof: "TestWatch_AFailedReadIsAnErrorAndNeverWaiting", Needle: "UnresolvedThreads"},
	"internal/review.Trees.CommitTree":         {Proof: "TestStanding_ATreeThatCannotBeReadIsAReasonNotAPass", Needle: "trees"},

	"internal/claim.Rows.HeldRows": {Proof: "TestEveryReadAndWriteThatFailsIsARefusalThatKeepsItsCause", Needle: "HeldRows"},
	"internal/claim.Rows.Reclaim":  {Proof: "TestEveryReadAndWriteThatFailsIsARefusalThatKeepsItsCause", Needle: "Reclaim"},

	"internal/flow.queueReader.OpenItems": {Proof: "TestQueue_AFailedReadFailsTheStepAndReturnsNoGuess", Needle: "itemsErr"},
	"internal/flow.queueReader.OpenPulls": {Proof: "TestQueue_AFailedReadFailsTheStepAndReturnsNoGuess", Needle: "pullsErr"},
	"internal/flow.orchestrators.Others":  {Proof: "TestRun_FailsWhenTheOtherOrchestratorsCannotBeListed", Needle: "fakeOthers"},

	// Methods that start, create or record something. The error is the result of the action, not a read that a guard depends on.
	"internal/batch.Recorder.Step":   {Exempt: doesWork},
	"internal/cli.Runner.RunFile":    {Exempt: doesWork},
	"internal/flow.agents.Run":       {Exempt: doesWork},
	"internal/flow.logins.Pick":      {Exempt: doesWork},
	"internal/flow.lifecycle.Claim":  {Exempt: doesWork},
	"internal/flow.beats.Beat":       {Exempt: doesWork},
	"internal/flow.beats.Renewed":    {Exempt: doesWork},
	"internal/flow.commands.Run":     {Exempt: doesWork},
	"internal/flow.hostKeeper.Up":    {Exempt: doesWork},
	"internal/flow.writer.Create":    {Exempt: doesWork},
	"internal/flow.treeMaker.Ensure": {Exempt: doesWork},
	"internal/forge.Opener.CreatePR": {Exempt: doesWork},
	"internal/sandbox.Enforcer.Run":  {Exempt: doesWork},
	"internal/store.Writer.Create":   {Exempt: doesWork},

	// Reads with no failed-read case at the consumer in this test. Each method has its own entry, so a new method on one of these interfaces fails the test.
	"internal/batch.Git.Version":                  {NotHeld: inAdapter},
	"internal/batch.Git.HasCommit":                {NotHeld: inAdapter},
	"internal/batch.Git.MergeTree":                {NotHeld: inAdapter},
	"internal/batch.Git.NewCommit":                {NotHeld: inAdapter},
	"internal/batch.Gate.Run":                     {NotHeld: inAdapter},
	"internal/batch.Base.Tip":                     {NotHeld: inAdapter},
	"internal/batch.Base.Merge":                   {NotHeld: inAdapter},
	"internal/batch.IssueReads.IssueState":        {NotHeld: inAdapter},
	"internal/batch.IssueReads.ClosingRefs":       {NotHeld: inAdapter},
	"internal/cli.Reading.Status":                 {NotHeld: noEngine},
	"internal/cli.Reading.Steps":                  {NotHeld: noEngine},
	"internal/cli.Reading.Lanes":                  {NotHeld: noEngine},
	"internal/cli.Reading.Poll":                   {NotHeld: noEngine},
	"internal/cli.Reading.PollNotScheduled":       {NotHeld: noEngine},
	"internal/cli.Reading.PinnedFile":             {NotHeld: noEngine},
	"internal/cli.ItemStore.Item":                 {NotHeld: noEngine},
	"internal/cli.ItemStore.OpenItems":            {NotHeld: noEngine},
	"internal/flow.batchHost.Issues":              {NotHeld: inAdapter},
	"internal/flow.batchHost.Git":                 {NotHeld: inAdapter},
	"internal/flow.batchHost.Checkout":            {NotHeld: inAdapter},
	"internal/flow.batchHost.Tip":                 {NotHeld: inAdapter},
	"internal/flow.batchHost.Intent":              {NotHeld: inAdapter},
	"internal/flow.gate.Submit":                   {NotHeld: inAdapter},
	"internal/flow.gate.Run":                      {NotHeld: inAdapter},
	"internal/flow.gate.Key":                      {NotHeld: inAdapter},
	"internal/flow.items.Fields":                  {NotHeld: inAdapter},
	"internal/flow.merger.Merge":                  {NotHeld: inAdapter},
	"internal/flow.merger.Poll":                   {NotHeld: inAdapter},
	"internal/flow.pollReader.Source":             {NotHeld: inAdapter},
	"internal/flow.preflighter.Preflight":         {NotHeld: inAdapter},
	"internal/flow.forge.Pull":                    {NotHeld: inAdapter},
	"internal/flow.forge.Reviewed":                {NotHeld: inAdapter},
	"internal/flow.forge.ClosingIssues":           {NotHeld: inAdapter},
	"internal/flow.forge.Comment":                 {NotHeld: inAdapter},
	"internal/flow.forge.Open":                    {NotHeld: inAdapter},
	"internal/flow.forge.PlaceCard":               {NotHeld: inAdapter},
	"internal/flow.forge.ReadCard":                {NotHeld: inAdapter},
	"internal/flow.forge.Assign":                  {NotHeld: inAdapter},
	"internal/flow.trees.Path":                    {NotHeld: inAdapter},
	"internal/flow.watcher.Watch":                 {NotHeld: inAdapter},
	"internal/forge.Boarder.CardStatus":           {NotHeld: inAdapter},
	"internal/forge.ItemReads.Item":               {NotHeld: inAdapter},
	"internal/forge.Standing.OwnPulls":            {NotHeld: inAdapter},
	"internal/forge.PullReads.Reviews":            {NotHeld: inAdapter},
	"internal/forge.PullReads.PRBody":             {NotHeld: inAdapter},
	"internal/forge.PullReads.PRView":             {NotHeld: inAdapter},
	"internal/forge.PullReads.PRList":             {NotHeld: inAdapter},
	"internal/forge.PullReads.PRChecks":           {NotHeld: inAdapter},
	"internal/forge.PullReads.OpenChangeRequests": {NotHeld: inAdapter},
	"internal/forge.PullReads.UnresolvedThreads":  {NotHeld: inAdapter},
	"internal/forge.PullReads.StackID":            {NotHeld: inAdapter},
	"internal/forge.PullReads.ExistingReview":     {NotHeld: inAdapter},
	"internal/forge.PullReads.Overlap":            {NotHeld: inAdapter},
	"internal/forge.Activity.Comments":            {NotHeld: inAdapter},
	"internal/forge.Activity.ReviewComments":      {NotHeld: inAdapter},
	"internal/forge.Lookup.PullCounts":            {NotHeld: inAdapter},
	"internal/forge.Lookup.Committers":            {NotHeld: inAdapter},
	"internal/forge.Lookup.TokenScopes":           {NotHeld: inAdapter},
	"internal/forge.Lookup.Search":                {NotHeld: inAdapter},
	"internal/forge.Lookup.DefaultBranch":         {NotHeld: inAdapter},
	"internal/forge.Lookup.BranchRef":             {NotHeld: inAdapter},
	"internal/forge.Lookup.Title":                 {NotHeld: inAdapter},
	"internal/forge.Lookup.CheckNotStarted":       {NotHeld: inAdapter},
	"internal/forge.Lookup.ClosingRefs":           {NotHeld: inAdapter},
	"internal/forge.Lookup.IssueState":            {NotHeld: inAdapter},
	"internal/gate.Remote.Submit":                 {NotHeld: inAdapter},
	"internal/gate.Remote.Wait":                   {NotHeld: inAdapter},
	"internal/gate.Remote.WorkRoot":               {NotHeld: inAdapter},
	"internal/gate.LogReader.Log":                 {NotHeld: inAdapter},
	"internal/host.Host.Status":                   {NotHeld: inAdapter},
	"internal/host.Host.Submit":                   {NotHeld: inAdapter},
	"internal/host.Host.Wait":                     {NotHeld: inAdapter},
	"internal/host.Host.WorkRoot":                 {NotHeld: inAdapter},
	"internal/host.Host.Log":                      {NotHeld: inAdapter},
	"internal/store.Tracker.Item":                 {NotHeld: inAdapter},
	"internal/store.Tracker.OpenItems":            {NotHeld: inAdapter},
	"internal/store.ClaimRows.HeldRows":           {NotHeld: inAdapter},
	"internal/store.ClaimRows.Reclaim":            {NotHeld: inAdapter},
	"cmd/obee.treeFinder.Path":                    {NotHeld: inAdapter},
	"cmd/obee.loginReader.Login":                  {NotHeld: noEngine},
}

// read is one method of one interface that returns a value with an error.
type read struct {
	dir   string
	iface string
	name  string
}

func (r read) key() string { return r.dir + "." + r.iface + "." + r.name }

// discover walks the roots and returns every method that returns a value with an error, of every interface declared in a Go file that is no test.
func discover(t *testing.T) []read {
	t.Helper()

	var found []read

	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join("..", "..", root), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if entry.IsDir() && entry.Name() == "testdata" {
				return filepath.SkipDir
			}

			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			reads, err := readsOfFile(path)
			found = append(found, reads...)

			return err
		})
		require.NoError(t, err)
	}

	return found
}

// readsOfFile parses one Go file and returns its reads, with the directory of the file relative to the root of the repository.
func readsOfFile(path string) ([]read, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(filepath.Join("..", ".."), filepath.Dir(path))
	if err != nil {
		return nil, err
	}

	return readsOf(parsed, filepath.ToSlash(rel)), nil
}

// readsOf returns the methods with a value and an error of every interface type declared in a parsed file.
func readsOf(parsed *ast.File, dir string) []read {
	var found []read

	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}

		for _, spec := range gen.Specs {
			if typed, ok := spec.(*ast.TypeSpec); ok {
				found = append(found, interfaceReads(typed, dir)...)
			}
		}
	}

	return found
}

// interfaceReads returns the methods of a type declaration that return a value with an error. A type that is no interface has none.
func interfaceReads(typed *ast.TypeSpec, dir string) []read {
	iface, ok := typed.Type.(*ast.InterfaceType)
	if !ok {
		return nil
	}

	var found []read

	for _, field := range iface.Methods.List {
		fn, ok := field.Type.(*ast.FuncType)
		if !ok || !returnsValueAndError(fn) {
			continue
		}

		for _, name := range field.Names {
			found = append(found, read{dir: dir, iface: typed.Name.Name, name: name.Name})
		}
	}

	return found
}

// returnsValueAndError is true when a method returns at least one value and then an error.
func returnsValueAndError(fn *ast.FuncType) bool {
	if fn.Results == nil {
		return false
	}

	count := 0
	for _, result := range fn.Results.List {
		count += max(1, len(result.Names))
	}

	last, ok := fn.Results.List[len(fn.Results.List)-1].Type.(*ast.Ident)

	return count >= 2 && ok && last.Name == "error"
}

// bodies returns every test function of the Go test files of a directory, by name.
func bodies(t *testing.T, dir string) map[string]*ast.BlockStmt {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("..", "..", dir, "*_test.go"))
	require.NoError(t, err)

	found := map[string]*ast.BlockStmt{}

	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		require.NoError(t, err)

		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				found[fn.Name.Name] = fn.Body
			}
		}
	}

	return found
}

// injects is true when a test body names the failure of a read as code: a string literal that holds needle, a key of a composite literal,
// the type of a composite literal, or the field that an assignment sets. Comments are not in the tree, and the name of a call does not count.
func injects(body *ast.BlockStmt, needle string) bool {
	hit := false

	ast.Inspect(body, func(node ast.Node) bool {
		hit = hit || namesFailure(node, needle)

		return !hit
	})

	return hit
}

// namesFailure is true when one node is a string literal that holds needle, a key or a type of a composite literal, or an assigned field named needle.
func namesFailure(node ast.Node, needle string) bool {
	switch n := node.(type) {
	case *ast.BasicLit:
		return n.Kind == token.STRING && strings.Contains(n.Value, needle)
	case *ast.KeyValueExpr:
		return isIdent(n.Key, needle)
	case *ast.CompositeLit:
		return isIdent(n.Type, needle)
	case *ast.AssignStmt:
		return slices.ContainsFunc(n.Lhs, func(left ast.Expr) bool {
			selector, ok := left.(*ast.SelectorExpr)

			return ok && selector.Sel.Name == needle
		})
	}

	return false
}

// isIdent is true when an expression is the plain name.
func isIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)

	return ok && ident.Name == name
}

// TestEveryReadOfEveryInterfaceIsClassified walks internal and cmd, finds every interface method that returns a value with an error, and fails for one that is in no rule.
// A rule is a proof (a test that injects the failure of the read and asserts the refusal), or a reason: the read is no guard (Exempt), or the case is not proven here (NotHeld).
// A new package or a new interface fails the test with "a read with no failed-read case". A rule for a method that is gone fails too.
func TestEveryReadOfEveryInterfaceIsClassified(t *testing.T) {
	t.Parallel()

	found := discover(t)
	require.NotEmpty(t, found, "the walk found no interface")

	used := map[string]bool{}

	for _, r := range found {
		key := r.key()
		rule, ok := rules[key]
		used[key] = true

		if !assert.True(t, ok, "%s: a read with no failed-read case. Add a test that fails it and name the test in rules, or state why it is exempt or not held", key) {
			continue
		}

		checkRule(t, r, rule)
	}

	var stale []string

	for key := range rules {
		if !used[key] {
			stale = append(stale, key)
		}
	}

	sort.Strings(stale)
	assert.Empty(t, stale, "rules for methods or interfaces that the tree does not have")
}

// checkRule checks that a rule holds one of its three forms, and that a proof is a test that injects the failure.
func checkRule(t *testing.T, r read, rule rule) {
	t.Helper()

	switch {
	case rule.Proof != "":
		if !assert.NotEmpty(t, rule.Needle, "%s: the proof %s names no failure to inject", r.key(), rule.Proof) {
			return
		}

		body, exists := bodies(t, r.dir)[rule.Proof]
		if !assert.True(t, exists, "%s: the test %s does not exist in %s", r.key(), rule.Proof, r.dir) {
			return
		}

		assert.True(t, injects(body, rule.Needle), "%s: the test %s does not inject a failure named %q", r.key(), rule.Proof, rule.Needle)
	case rule.Exempt != "" || rule.NotHeld != "":
	default:
		assert.Fail(t, "a rule with no proof and no reason", r.key())
	}
}
