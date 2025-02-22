package output

import (
	"fmt"
	"log"
	"net/url"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/sirupsen/logrus"
	"github.com/trufflesecurity/trufflehog/v3/pkg/detectors"
	"github.com/trufflesecurity/trufflehog/v3/pkg/pb/sourcespb"
)

func ConvertToLegacyJSON(r *detectors.ResultWithMetadata, repoPath string) *LegacyJSONOutput {
	var source LegacyJSONCompatibleSource
	switch r.SourceType {
	case sourcespb.SourceType_SOURCE_TYPE_GIT:
		source = r.SourceMetadata.GetGit()
	case sourcespb.SourceType_SOURCE_TYPE_GITHUB:
		source = r.SourceMetadata.GetGithub()
	case sourcespb.SourceType_SOURCE_TYPE_GITLAB:
		source = r.SourceMetadata.GetGitlab()
	default:
		log.Fatalf("legacy JSON output can not be used with this source: %s", r.SourceName)
	}

	// The repo will be needed to gather info needed for the legacy output that isn't included in the new
	// output format.
	repo, err := gogit.PlainOpenWithOptions(repoPath, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		logrus.WithError(err).Fatalf("could not open repo: %s", repoPath)
	}

	fileName := source.GetFile()
	commitHash := plumbing.NewHash(source.GetCommit())
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		log.Fatal(err)
	}

	diff := GenerateDiff(commit, fileName)

	foundString := string(r.Result.Raw)

	// Add highlighting to the offending bit of string.
	printableDiff := strings.ReplaceAll(diff, foundString, fmt.Sprintf("\u001b[93m%s\u001b[0m", foundString))

	// Load up the struct to match the old JSON format
	output := &LegacyJSONOutput{
		Branch:       FindBranch(commit, repo),
		Commit:       commit.Message,
		CommitHash:   commitHash.String(),
		Date:         commit.Committer.When.Format("2006-01-02 15:04:05"),
		Diff:         diff,
		Path:         fileName,
		PrintDiff:    printableDiff,
		Reason:       r.Result.DetectorType.String(),
		StringsFound: []string{foundString},
	}
	return output
}

// BranchHeads creates a map of branch names to their head commit. This can be used to find if a commit is an ancestor
// of a branch head.
func BranchHeads(repo *gogit.Repository) (map[string]*object.Commit, error) {
	branches := map[string]*object.Commit{}
	branchIter, err := repo.Branches()
	if err != nil {
		return branches, err
	}

	err = branchIter.ForEach(func(branchRef *plumbing.Reference) error {
		branchName := branchRef.Name().String()
		headHash, err := repo.ResolveRevision(plumbing.Revision(branchName))
		if err != nil {
			logrus.WithError(err).Errorf("unable to resolve head of branch: %s", branchRef.Name().String())
			return nil
		}
		headCommit, err := repo.CommitObject(*headHash)
		if err != nil {
			logrus.WithError(err).Errorf("unable to get commit: %s", headCommit.String())
			return nil
		}
		branches[branchName] = headCommit
		return nil
	})
	return branches, err
}

// FindBranch returns the first branch a commit is a part of. Not the most accurate, but it should work similar to pre v3.0.
func FindBranch(commit *object.Commit, repo *gogit.Repository) string {
	branches, err := BranchHeads(repo)
	if err != nil {
		logrus.WithError(err).Fatal("could not list branches")
	}

	for name, head := range branches {
		isAncestor, err := commit.IsAncestor(head)
		if err != nil {
			logrus.WithError(err).Errorf("could not determine if %s is an ancestor of %s", commit.Hash.String(), head.Hash.String())
			continue
		}
		if isAncestor {
			return name
		}
	}
	return ""
}

// GenerateDiff will take a commit and create a string diff between the commit and its first parent.
func GenerateDiff(commit *object.Commit, fileName string) string {
	var diff string

	// First grab the first parent of the commit. If there are none, we are at the first commit and should diff against
	// an empty file.
	parent, err := commit.Parent(0)
	if err != object.ErrParentNotFound && err != nil {
		logrus.WithError(err).Errorf("could not find parent of %s", commit.Hash.String())
	}

	// Now get the files from the commit and its parent.
	var parentFile *object.File
	if parent != nil {
		parentFile, err = parent.File(fileName)
		if err != nil && err != object.ErrFileNotFound {
			logrus.WithError(err).Errorf("could not get previous version of file: %q", fileName)
			return diff
		}
	}
	commitFile, err := commit.File(fileName)
	if err != nil {
		logrus.WithError(err).Errorf("could not get current version of file: %q", fileName)
		return diff
	}

	// go-git doesn't support creating a diff for just one file in a commit, so another package is needed to generate
	// the diff.
	dmp := diffmatchpatch.New()
	var oldContent, newContent string
	if parentFile != nil {
		oldContent, err = parentFile.Contents()
		if err != nil {
			logrus.WithError(err).Errorf("could not get contents of previous version of file: %q", fileName)
		}
	}
	// commitFile should never be nil at this point, but double-checking so we don't get a nil error.
	if commitFile != nil {
		newContent, _ = commitFile.Contents()
		if err != nil {
			logrus.WithError(err).Errorf("could not get contents of current version of file: %q", fileName)
		}
	}

	// If anything has gone wrong here, we'll just be diffing two empty files.
	diffs := dmp.DiffMain(oldContent, newContent, false)
	patches := dmp.PatchMake(diffs)

	// Put all the pieces of the diff together into one string.
	for _, patch := range patches {
		// The String() method URL escapes the diff, so it needs to be undone.
		patchDiff, err := url.QueryUnescape(patch.String())
		if err != nil {
			logrus.WithError(err).Error("unable to unescape diff")
		}
		diff += patchDiff
	}
	return diff
}

type LegacyJSONOutput struct {
	Branch       string   `json:"branch"`
	Commit       string   `json:"commit"`
	CommitHash   string   `json:"commitHash"`
	Date         string   `json:"date"`
	Diff         string   `json:"diff"`
	Path         string   `json:"path"`
	PrintDiff    string   `json:"printDiff"`
	Reason       string   `json:"reason"`
	StringsFound []string `json:"stringsFound"`
}

type LegacyJSONCompatibleSource interface {
	GetCommit() string
	GetFile() string
}
