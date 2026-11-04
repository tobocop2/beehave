package takeover

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	noticePrefix = "Taking over this pull request"
	fields       = "number,state,isDraft,author,assignees,updatedAt,headRefOid,commits,comments,reviews"
	ageStep      = 36 * time.Second // the age is kept to two decimals of an hour
)

var datePattern = regexp.MustCompile(`^[0-9]{4}-`)

// halt ends a step with a refusal. Run turns it into a Result that keeps the lines said before it.
type halt struct {
	code  int
	lines []string
}

func (h *halt) Error() string { return strings.Join(h.lines, "\n") }

func stop(code int, lines ...string) *halt { return &halt{code: code, lines: lines} }

func unread(cause error, format string, args ...any) error {
	return errors.Join(&check.Refusal{Message: fmt.Sprintf(format, args...)}, cause)
}

// Run runs one step of the takeover. A refusal of a rule is a Result with a code; a read that fails is an error.
func Run(ctx context.Context, reader Reader, request Request, c Context) (Result, error) {
	s := &session{reader: reader, request: request, c: c}

	err := s.run(ctx)

	var stopped *halt
	if errors.As(err, &stopped) {
		return Result{Code: stopped.code, Lines: append(s.lines, stopped.lines...)}, nil
	}

	return Result{Lines: s.lines}, err
}

type session struct {
	reader  Reader
	request Request
	c       Context
	lines   []string
}

func (s *session) say(lines ...string) { s.lines = append(s.lines, lines...) }

// load reads the pull request, guards it and plans the run.
func (s *session) load(ctx context.Context) (plan, error) {
	pr, err := s.reader.PRView(ctx, s.request.PR, strings.Split(fields, ",")...)
	if err != nil {
		return plan{}, unread(err, "cannot read #%d.", s.request.PR)
	}

	if err = s.guard(pr); err != nil {
		return plan{}, err
	}

	rows, err := s.reader.ReviewComments(ctx, s.request.PR)
	if err != nil {
		return plan{}, unread(err, "cannot read the review comments of #%d.", s.request.PR)
	}

	return s.plan(pr, rows)
}

func (s *session) run(ctx context.Context) error {
	plan, err := s.load(ctx)
	if err != nil {
		return err
	}

	switch s.request.Mode {
	case ModeApply:
		return s.apply(ctx, plan)
	case ModeTake:
		return s.take(ctx, plan)
	case ModeDry:
	}

	s.dry(plan)

	return nil
}

// guard refuses a pull request that is not open, is ours, or has moved from the head the caller works on.
func (s *session) guard(pr model.PullRequest) error {
	number := s.request.PR

	if state := orNull(pr.State); state != "OPEN" {
		return stop(CodeGuard, fmt.Sprintf("REFUSE: #%d is %s, not OPEN", number, state))
	}

	if pr.Author.Login == s.c.Me {
		return stop(CodeGuard, fmt.Sprintf("REFUSE: #%d is yours. Your own work needs an independent verdict, not a claim.", number))
	}

	return s.checkHead(pr)
}

// checkHead refuses a head that is not the one the caller works on. With no head named, any head passes.
func (s *session) checkHead(pr model.PullRequest) error {
	number := s.request.PR

	want := s.request.HeadWant
	if want == "" {
		return nil
	}

	if pr.HeadRefOid == nil || *pr.HeadRefOid == "" {
		return stop(CodeGuard, fmt.Sprintf("REFUSE: cannot read the head of #%d from the forge.", number))
	}

	if *pr.HeadRefOid != want {
		return stop(CodeRule, fmt.Sprintf("REFUSE: the head of #%d is %s, not %s. The author pushed. Start from the new head.", number, *pr.HeadRefOid, want))
	}

	return nil
}

func orNull(text *string) string {
	if text == nil {
		return model.OrNull("")
	}

	return model.OrNull(*text)
}

// stamp is a time from the forge with the text the forge sent.
type stamp struct {
	at   time.Time
	text string
}

// parseStamp reads a time. A text that does not start like a date is not a timestamp. A date that is not a UTC stamp is a refusal.
func parseStamp(text string) (stamp, bool, error) {
	if !datePattern.MatchString(text) {
		return stamp{}, false, nil
	}

	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return stamp{}, false, &check.Refusal{Message: fmt.Sprintf("cannot read the time '%s' as a UTC timestamp.", text)}
	}

	return stamp{at: at, text: text}, true, nil
}

// entry is a comment, a review or a review comment of somebody who is not us.
type entry struct {
	when  stamp
	who   string
	label string
	body  string
}

// push is a commit of somebody who is not us. A commit of two such authors is two pushes.
type push struct {
	when stamp
	who  string
	head string
}

// activity is what the forge says about the pull request, split into the parts the rules read.
type activity struct {
	humans   []entry
	pushes   []push
	notice   *stamp
	reviewed bool
	stamps   []stamp
}

func (s *session) read(pr model.PullRequest, rows []model.Comment) (activity, error) {
	var found activity

	if err := s.readComments(&found, pr); err != nil {
		return found, err
	}

	if err := s.readReviews(&found, pr); err != nil {
		return found, err
	}

	if err := s.readRows(&found, rows); err != nil {
		return found, err
	}

	return found, s.readCommits(&found, pr)
}

// human records what somebody else said. A missing time is a draft and is skipped.
func (a *activity) human(text *string, who, label, body string) error {
	if text == nil {
		return nil
	}

	when, ok, err := parseStamp(*text)
	if ok {
		a.humans = append(a.humans, entry{when, who, label, body})
	}

	return err
}

func (s *session) readComments(found *activity, pr model.PullRequest) error {
	for _, comment := range pr.Comments {
		if comment.Author.Login == s.c.Me {
			if err := found.keepNotice(comment.Body, comment.CreatedAt); err != nil {
				return err
			}

			continue
		}

		if err := found.human(comment.CreatedAt, comment.Author.Login, "comment", comment.Body); err != nil {
			return err
		}
	}

	return nil
}

func (s *session) readReviews(found *activity, pr model.PullRequest) error {
	for _, review := range pr.Reviews {
		if review.Author.Login == s.c.Me {
			found.reviewed = found.reviewed || review.State == model.ReviewChangesRequested

			continue
		}

		if err := found.human(review.SubmittedAt, review.Author.Login, "review "+string(review.State), review.Body); err != nil {
			return err
		}
	}

	return nil
}

func (s *session) readRows(found *activity, rows []model.Comment) error {
	for _, row := range rows {
		if row.User.Login == s.c.Me {
			continue
		}

		if err := found.human(&row.CreatedAt, row.User.Login, "review comment on "+row.Path, row.Body); err != nil {
			return err
		}
	}

	return nil
}

// keepNotice remembers the latest comment of ours that is a notice.
func (a *activity) keepNotice(body string, created *string) error {
	if created == nil || !strings.HasPrefix(body, noticePrefix) {
		return nil
	}

	when, ok, err := parseStamp(*created)
	if ok && (a.notice == nil || when.at.After(a.notice.at)) {
		a.notice = &when
	}

	return err
}

func (s *session) readCommits(found *activity, pr model.PullRequest) error {
	for _, commit := range pr.Commits {
		when, ok, err := parseStamp(commit.CommittedDate)
		if err != nil {
			return err
		}

		if !ok {
			continue
		}

		logins := make([]string, len(commit.Authors))
		for i, author := range commit.Authors {
			logins[i] = author.Login
		}

		who, copies := s.authorsOf(logins)
		found.addPushes(push{when, who, commit.MessageHeadline}, copies)
	}

	return nil
}

// addPushes records one push for each copy.
func (a *activity) addPushes(one push, copies int) {
	for range copies {
		a.pushes = append(a.pushes, one)
	}
}

// unknown is the name of a push whose commit has no author with a login.
const unknown = "unknown"

// authorsOf names the first author with a login and counts one push for each author who is not us. A commit with no author is one push by unknown.
func (s *session) authorsOf(logins []string) (string, int) {
	if len(logins) == 0 {
		return unknown, 1
	}

	who, copies := unknown, 0

	for _, login := range logins {
		if login != "" && who == unknown {
			who = login
		}

		if login != s.c.Me {
			copies++
		}
	}

	return who, copies
}

// plan says what the activity means for this run.
type plan struct {
	activity
	assigned bool
	age      time.Duration
	idle     bool
	notice   string
}

func (s *session) plan(pr model.PullRequest, rows []model.Comment) (plan, error) {
	found, err := s.read(pr, rows)
	if err != nil {
		return plan{}, err
	}

	if err = s.sortStamps(&found); err != nil {
		return plan{}, err
	}

	names := assigneeNames(pr)
	newest := found.stamps[len(found.stamps)-1]
	p := plan{activity: found, assigned: slices.Contains(names, s.c.Me), age: s.age(newest)}
	p.idle = p.age >= s.c.Idle
	p.notice = s.noticeText(p.assigned, p.age)

	s.report(pr, p, names)

	return p, nil
}

func assigneeNames(pr model.PullRequest) []string {
	names := make([]string, len(pr.Assignees))
	for i, assignee := range pr.Assignees {
		names[i] = assignee.Login
	}

	return names
}

// report says who holds the pull request and when it was touched last.
func (s *session) report(pr model.PullRequest, p plan, names []string) {
	newest := p.stamps[len(p.stamps)-1]

	s.say(fmt.Sprintf("pr %d  author %s  assignees [%s]  basis %s  last human activity %s (%sh ago, from %d timestamps)",
		s.request.PR, model.OrNull(pr.Author.Login), strings.Join(names, ","), basisOf(p.assigned), newest.text, hours(p.age), len(p.stamps)))

	if p.activity.notice != nil {
		s.say("notice already posted at " + p.activity.notice.text)
	}
}

// sortStamps gathers every human and push time, oldest first. No time at all is an unreadable pull request, not an idle one.
func (s *session) sortStamps(found *activity) error {
	for _, entry := range found.humans {
		found.stamps = append(found.stamps, entry.when)
	}

	for _, push := range found.pushes {
		found.stamps = append(found.stamps, push.when)
	}

	if len(found.stamps) == 0 {
		return stop(CodeGuard, fmt.Sprintf("REFUSE: #%d produced no activity timestamps at all. That is an unreadable pull request, not an idle one.", s.request.PR))
	}

	slices.SortStableFunc(found.stamps, func(a, b stamp) int { return a.at.Compare(b.at) })

	return nil
}

func basisOf(assigned bool) string {
	if assigned {
		return "assigned"
	}

	return "idle"
}

// age is the time from a stamp to now, kept to two decimals of an hour.
func (s *session) age(then stamp) time.Duration { return s.c.Now.Sub(then.at).Round(ageStep) }

func hours(d time.Duration) string { return fmt.Sprintf("%.2f", d.Hours()) }

func setting(d time.Duration) string { return strconv.FormatFloat(d.Hours(), 'f', -1, 64) }

func (s *session) noticeText(assigned bool, age time.Duration) string {
	why := block(whyIdleMD)
	if assigned {
		why = block(whyAssignedMD)
	}

	why = strings.NewReplacer("{age}", hours(age), "{idle}", setting(s.c.Idle)).Replace(why)

	return strings.NewReplacer("{why}", why, "{grace}", setting(s.c.Grace)).Replace(block(noticeMD))
}

func (s *session) needReview(p plan) error {
	if p.reviewed {
		return nil
	}

	return stop(CodeRule, fmt.Sprintf("REFUSE: no changes-requested review of yours is on #%d. Post the review first, then ask.", s.request.PR))
}

func (s *session) dry(p plan) {
	switch {
	case !p.reviewed:
		s.say(fmt.Sprintf("WOULD DO NOTHING: no changes-requested review of yours is on #%d. Post the review first.", s.request.PR))
	case p.assigned || p.idle:
		s.say("WOULD POST: "+p.notice, "(nothing written; re-run with --apply)")
	default:
		s.say(fmt.Sprintf("WOULD DO NOTHING: not assigned to you, and touched %sh ago, inside the %sh threshold.", hours(p.age), setting(s.c.Idle)))
	}
}

// mayPost refuses a notice that has no review behind it or no right to be posted yet.
func (s *session) mayPost(p plan) error {
	if err := s.needReview(p); err != nil {
		return err
	}

	if !p.assigned && !p.idle {
		return stop(CodeRule, fmt.Sprintf("REFUSE: #%d is not assigned to you and was touched %sh ago, inside the %sh threshold. Nothing posted.", s.request.PR, hours(p.age), setting(s.c.Idle)))
	}

	return nil
}

// reread reads the comments with the state, the author and the head, and runs the guard on them.
func (s *session) reread(ctx context.Context) (model.PullRequest, error) {
	pr, err := s.reader.PRView(ctx, s.request.PR, "comments", "state", "author", "headRefOid")
	if err != nil {
		return model.PullRequest{}, unread(err, "cannot read the comments of #%d.", s.request.PR)
	}

	return pr, s.guard(pr)
}

// posted asks the forge for the notice of ours on the pull request. A read that fails is an error, never "no notice".
func (s *session) posted(ctx context.Context) (*stamp, error) {
	pr, err := s.reread(ctx)
	if err != nil {
		return nil, err
	}

	var found activity

	for _, comment := range pr.Comments {
		if comment.Author.Login != s.c.Me {
			continue
		}

		if err = found.keepNotice(comment.Body, comment.CreatedAt); err != nil {
			return nil, err
		}
	}

	return found.notice, nil
}

// apply posts the notice once. A notice that the forge already holds is used, not posted again, so a step that repeats after a crash does not post twice.
// The forge is asked again right before the write, because the first read is older than the claim check.
func (s *session) apply(ctx context.Context, p plan) error {
	if err := s.mayPost(p); err != nil {
		return err
	}

	if err := s.c.Free(ctx); err != nil {
		return err
	}

	existing, err := s.posted(ctx)
	if err != nil {
		return err
	}

	if existing != nil {
		s.say(fmt.Sprintf("notice already posted on #%d at %s. Nothing written. Take it no earlier than %sh after that.", s.request.PR, existing.text, setting(s.c.Grace)))

		return nil
	}

	return s.post(ctx, p)
}

func (s *session) post(ctx context.Context, p plan) error {
	number := s.request.PR

	if err := s.reader.PostComment(ctx, number, p.notice); err != nil {
		return unread(err, "cannot post the notice on #%d.", number)
	}

	s.say(fmt.Sprintf("notice posted on #%d (basis: %s). Take it no earlier than %sh from now, and only if nobody forbids it.", number, basisOf(p.assigned), setting(s.c.Grace)))

	return nil
}

// aged returns the notice and its age. It refuses a take with no review of ours, no notice, or a notice younger than the grace period.
func (s *session) aged(p plan) (stamp, time.Duration, error) {
	if err := s.needReview(p); err != nil {
		return stamp{}, 0, err
	}

	if p.activity.notice == nil {
		return stamp{}, 0, stop(CodeRule, fmt.Sprintf("REFUSE: no notice has been posted on #%d. Announce first, then wait. Assignment makes it ours to finish; it does not excuse the notice.", s.request.PR))
	}

	noticed := *p.activity.notice

	since := s.age(noticed)
	if since < s.c.Grace {
		return stamp{}, 0, stop(CodeRule, fmt.Sprintf("REFUSE: the notice is only %sh old and the grace period is %sh.", hours(since), setting(s.c.Grace)))
	}

	return noticed, since, nil
}

func (s *session) take(ctx context.Context, p plan) error {
	noticed, since, err := s.aged(p)
	if err != nil {
		return err
	}

	if err = s.verdict(p, noticed); err != nil {
		return err
	}

	// The take writes no claim. A claim lapses after 5 minutes unless something renews it, and this step exits at once.
	// The run of the item in the engine takes the claim and renews it. This step only reads, so it can refuse a claim that another owner holds.
	if err = s.c.Free(ctx); err != nil {
		return err
	}

	s.say(fmt.Sprintf("#%d may be taken now (basis: %s) after a %sh grace. The run of the item holds the claim.", s.request.PR, basisOf(p.assigned), hours(since)))

	return nil
}

// verdict applies the tokens, the idle threshold and the replies that the caller did not read. RELEASE excuses the last two.
func (s *session) verdict(p plan, noticed stamp) error {
	released, err := s.tokens(p, noticed)
	if err != nil || released {
		return err
	}

	if !p.idle {
		return s.busy(p)
	}

	return s.settle(p, noticed)
}

func (s *session) busy(p plan) error {
	return stop(CodeRule,
		fmt.Sprintf("REFUSE: #%d was touched %sh ago, inside the %sh idle threshold.", s.request.PR, hours(p.age), setting(s.c.Idle)),
		"        Assignment does not shorten that wait: somebody is working on this right now.",
		"        If that somebody is handing it over, they can reply RELEASE.")
}

// settle refuses replies that the caller did not read.
func (s *session) settle(p plan, noticed stamp) error {
	newer := p.after(noticed)
	if newer == 0 {
		return nil
	}

	if !s.request.ReplyRead {
		return s.printReplies(p, noticed, newer)
	}

	s.say(fmt.Sprintf("note: %d replies landed after the notice, were read, and did not forbid this take.", newer))

	return nil
}

// after counts the stamps that are later than the notice.
func (p plan) after(noticed stamp) int {
	n := 0

	for _, when := range p.stamps {
		if when.at.After(noticed.at) {
			n++
		}
	}

	return n
}

// replies returns what was said after the notice.
func (p plan) replies(noticed stamp) []entry {
	return slices.DeleteFunc(slices.Clone(p.humans), func(e entry) bool { return !e.when.at.After(noticed.at) })
}

// replyText joins the bodies of the replies after the notice, one for each line.
func (p plan) replyText(noticed stamp) string {
	var bodies []string

	for _, reply := range p.replies(noticed) {
		bodies = append(bodies, reply.body)
	}

	return strings.Join(bodies, "\n")
}

// refusePush refuses a take when somebody pushed after the notice. A push is an objection with no words attached.
func (s *session) refusePush(p plan, noticed stamp) error {
	pushed := 0

	for _, push := range p.pushes {
		if push.when.at.After(noticed.at) {
			pushed++
		}
	}

	if pushed == 0 {
		return nil
	}

	return stop(CodeRule, fmt.Sprintf("REFUSE: somebody pushed %d commit(s) to #%d after the notice. A push is an objection with no words attached.", pushed, s.request.PR))
}

// tokens applies the KEEP and RELEASE protocol and the rule on a push after the notice. It returns true for RELEASE.
func (s *session) tokens(p plan, noticed stamp) (bool, error) {
	if err := s.refusePush(p, noticed); err != nil {
		return false, err
	}

	text := p.replyText(noticed)
	number := s.request.PR

	if says(keepWord, text) {
		return false, stop(CodeRule, fmt.Sprintf("REFUSE: a reply on #%d says KEEP. Leaving it alone.", number))
	}

	released := says(releaseWord, text)
	if released {
		s.say(fmt.Sprintf("RELEASE received on #%d: taking it now, without the grace period or the idle wait.", number))
	}

	return released, nil
}

func (s *session) printReplies(p plan, noticed stamp, newer int) error {
	lines := []string{
		fmt.Sprintf("REFUSE: %d replies landed on #%d and none used KEEP or RELEASE. Read the intent and decide.", newer, s.request.PR),
		"--- replies after " + noticed.text + " ---",
	}

	for _, reply := range p.replies(noticed) {
		lines = append(lines, fmt.Sprintf("[%s] %s (%s):\n%s\n", reply.when.text, model.OrNull(reply.who), reply.label, reply.body))
	}

	lines = append(lines, "--- end of replies ---",
		"STOP if the intent is to keep it: an objection, a claim of ownership, a push, or a request to wait.",
		"PROCEED only if the intent plainly allows it, by re-running: --take --reply-read")

	return stop(CodeRule, lines...)
}
