package batch

import "context"

// Held is a member that conflicts with an earlier member of the same union. With is that member.
type Held struct {
	Member Member `json:"member"`
	With   string `json:"with"`
}

// Union is the fold of members onto a base tip. Prefix[i] is the tree after Included[i], so a difference between git's merge and the forge's merge shows at the first member.
// Conflicts holds the members that conflict with the base tip alone, and Held those that conflict only with an earlier member.
type Union struct {
	Commit    string   `json:"commit"`
	Tree      string   `json:"tree"`
	Prefix    []string `json:"prefix"`
	Included  []Member `json:"included"`
	Held      []Held   `json:"held"`
	Conflicts []Member `json:"conflicts"`
}

// BuildUnion folds the members, in the order given, onto the tip with git merge-tree. The caller sorts the members.
func BuildUnion(ctx context.Context, git Git, tip Tip, members []Member) (Union, error) {
	f := folder{git: git, tip: tip, union: Union{Commit: tip.Commit, Tree: tip.Tree}}

	for _, m := range members {
		if err := f.add(ctx, m); err != nil {
			return Union{}, err
		}
	}

	return f.union, nil
}

type folder struct {
	git   Git
	tip   Tip
	union Union
}

func (f *folder) add(ctx context.Context, m Member) error {
	res, err := f.git.MergeTree(ctx, f.union.Commit, m.Head)
	if err != nil {
		return err
	}

	if res.Conflict {
		return f.refuse(ctx, m)
	}

	commit, err := f.git.NewCommit(ctx, res.Tree, f.union.Commit, m.Head)
	if err != nil {
		return err
	}

	f.union.Commit, f.union.Tree = commit, res.Tree
	f.union.Prefix = append(f.union.Prefix, res.Tree)
	f.union.Included = append(f.union.Included, m)

	return nil
}

// refuse sorts a conflicting member: alone on the tip it conflicts with the base, otherwise it conflicts with an earlier member.
func (f *folder) refuse(ctx context.Context, m Member) error {
	alone, err := f.git.MergeTree(ctx, f.tip.Commit, m.Head)
	if err != nil {
		return err
	}

	if alone.Conflict {
		f.union.Conflicts = append(f.union.Conflicts, m)

		return nil
	}

	with, err := f.culprit(ctx, m)
	if err != nil {
		return err
	}

	f.union.Held = append(f.union.Held, Held{Member: m, With: with})

	return nil
}

// culprit names the first earlier member that conflicts with m by itself. When only the fold of several conflicts, it names the last earlier member.
func (f *folder) culprit(ctx context.Context, m Member) (string, error) {
	for _, earlier := range f.union.Included {
		res, err := f.git.MergeTree(ctx, earlier.Head, m.Head)
		if err != nil {
			return "", err
		}

		if res.Conflict {
			return earlier.Item, nil
		}
	}

	if len(f.union.Included) == 0 {
		return "base", nil
	}

	return f.union.Included[len(f.union.Included)-1].Item, nil
}
