package gitstats

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/github"
	"github.com/gosimple/slug"
	. "github.com/intelsdi-x/snap-plugin-utilities/logger"
	"github.com/intelsdi-x/snap/control/plugin"
	"github.com/intelsdi-x/snap/control/plugin/cpolicy"
	"github.com/intelsdi-x/snap/core"
	"github.com/intelsdi-x/snap/core/ctypes"
)

const (
	// Name of plugin
	Name = "rt-gitstats"
	// Version of plugin
	Version = 1
	// Type of plugin
	Type = plugin.CollectorPluginType
)

// make sure that we actually satisify requierd interface
var _ plugin.CollectorPlugin = (*Gitstats)(nil)

var (
	repoMetricNames = []string{
		"forks",
		"issues",
		"network",
		"stars",
		"subscribers",
		"watches",
		"size",
	}
	userMetricNames = []string{
		"public_repos",
		"public_gists",
		"followers",
		"following",
		"private_repos",
		"private_gists",
		"plan_private_repos",
		"plan_seats",
		"plan_filled_seats",
	}
)

func init() {
	slug.CustomSub = map[string]string{".": "_"}
}

type Gitstats struct {
}

// CollectMetrics collects metrics for testing
func (f *Gitstats) CollectMetrics(mts []plugin.MetricType) ([]plugin.MetricType, error) {
	var err error

	conf := mts[0].Config().Table()
	accessToken, ok := conf["access_token"]
	if !ok || accessToken.(ctypes.ConfigValueStr).Value == "" {
		return nil, fmt.Errorf("access token missing from config, %v", conf)
	}

	metrics, err := gitStats(accessToken.(ctypes.ConfigValueStr).Value, mts)
	if err != nil {
		return nil, err
	}

	return metrics, nil
}

func gitStats(accessToken string, mts []plugin.MetricType) ([]plugin.MetricType, error) {
	ctx := context.Background()

	client := NewClient(accessToken)

	collectionTime := time.Now()
	repos := make(map[string]map[string]map[string]int)
	users := make(map[string]map[string]int)

	userRepos := make(map[string]struct{})
	authUser := ""
	useRepo := ""
	conf := mts[0].Config().Table()
	confUser, ok := conf["user"]
	if ok && confUser.(ctypes.ConfigValueStr).Value != "" {
		authUser = confUser.(ctypes.ConfigValueStr).Value
	}
	confRepo, ok := conf["repo"]
	if ok && confRepo.(ctypes.ConfigValueStr).Value != "" {
		useRepo = confRepo.(ctypes.ConfigValueStr).Value
	}

	metrics := make([]plugin.MetricType, 0)

	for _, m := range mts {
		ns := m.Namespace().Strings()
		fmt.Printf("getting %s\n", m.Namespace().String())
		switch ns[3] {
		case "repo":
			user := ns[4]
			repo := ns[5]
			stat := ns[6]

			if user == "*" {
				//need to get user
				if authUser == "" {
					gitUser, _, err := client.GetUsers(ctx, "")
					if err != nil {
						LogError("failed to get authenticated user.", err)
						return nil, err
					}
					stats, err := userStats(ctx, gitUser, client)
					if err != nil {
						LogError("failed to get stats from user object.", err)
						return nil, err
					}
					users[*gitUser.Login] = stats
					authUser = *gitUser.Login
				}
				user = authUser
			}
			if repo == "*" && useRepo == "" {
				// get list of all repos owned by the user.
				if _, ok := userRepos[user]; !ok {
					opt := &github.RepositoryListOptions{Type: "owner"}
					repoList, _, err := client.ListRepositories(ctx, user, opt)
					if err != nil {
						LogError("failed to get repos owned by user.", err)
						return nil, err
					}
					userRepos[user] = struct{}{}
					if _, ok := repos[user]; !ok {
						repos[user] = make(map[string]map[string]int)
					}
					for _, r := range repoList {
						repoSlug := slug.Make(*r.Name)

						if stat == "issuesbylabel" {
							labels, issues, err := client.GetAllLabelsAndIssues(ctx, user, repo)
							if err != nil {
								LogError("failed to get labels and issues from repo object.", err)
								return nil, err
							}

							issueMetrics, err := collectIssueMetrics(m, collectionTime, user, repoSlug, labels, issues)
							if err != nil {
								LogError("failed to get issue stats.", err)
								return nil, err
							}
							metrics = append(metrics, issueMetrics...)
						} else {
							stats, err := repoStats(r)
							if err != nil {
								LogError("failed to get stats from repo object.", err)
								return nil, err
							}
							repos[user][repoSlug] = stats
						}
					}
				}
				for repo, stats := range repos[user] {
					mt := plugin.MetricType{
						Data_:      stats[stat],
						Namespace_: core.NewNamespace("raintank", "apps", "gitstats", "repo", user, repo, stat),
						Timestamp_: collectionTime,
						Version_:   m.Version(),
					}
					metrics = append(metrics, mt)
				}
			} else {
				if repo == "*" {
					repo = useRepo
				}
				repoSlug := slug.Make(repo)

				if stat == "issuesbylabel" {
					labels, issues, err := client.GetAllLabelsAndIssues(ctx, user, repo)
					if err != nil {
						LogError("failed to get labels and issues from repo object.", err)
						return nil, err
					}

					issueMetrics, err := collectIssueMetrics(m, collectionTime, user, repoSlug, labels, issues)
					if err != nil {
						LogError("failed to get issue stats.", err)
						return nil, err
					}
					metrics = append(metrics, issueMetrics...)
				} else {

					if _, ok := repos[user]; !ok {
						repos[user] = make(map[string]map[string]int)
					}
					if _, ok := repos[user][repoSlug]; !ok {
						r, _, err := client.GetRepository(ctx, user, repo)
						if err != nil {
							LogError("failed to user repos.", err)
							return nil, err
						}

						stats, err := repoStats(r)
						if err != nil {
							LogError("failed to get stats from repo object.", err)
							return nil, err
						}
						repos[user][repoSlug] = stats
					}

					mt := plugin.MetricType{
						Data_:      repos[user][repo][stat],
						Namespace_: core.NewNamespace("raintank", "apps", "gitstats", "repo", user, repoSlug, stat),
						Timestamp_: collectionTime,
						Version_:   m.Version(),
					}
					metrics = append(metrics, mt)
				}
			}

		case "user":
			user := ns[4]
			stat := ns[5]
			if user == "*" {
				//need to get user
				if authUser == "" {
					gitUser, _, err := client.GetUsers(ctx, "")
					if err != nil {
						LogError("failed to get authenticated user.", err)
						return nil, err
					}
					authUser = *gitUser.Login
					stats, err := userStats(ctx, gitUser, client)
					if err != nil {
						LogError("failed to get stats from user object", err)
						return nil, err
					}
					users[authUser] = stats
				} else {
					if _, ok := users[authUser]; !ok {
						gitUser, _, err := client.GetUsers(ctx, authUser)
						if err != nil {
							LogError("failed to get authenticated user.", err)
							return nil, err
						}
						stats, err := userStats(ctx, gitUser, client)
						if err != nil {
							LogError("failed to get stats from user object", err)
							return nil, err
						}
						users[authUser] = stats
					}
				}
				user = authUser
			} else {
				if _, ok := users[user]; !ok {
					fmt.Printf("getting stats for user %s\n", user)
					u, _, err := client.GetUsers(ctx, user)
					if err != nil {
						LogError("failed to lookup user.", err)
						return nil, err
					}
					stats, err := userStats(ctx, u, client)
					if err != nil {
						LogError("failed to get stats from user object.", err)
						return nil, err
					}
					users[user] = stats
				}
			}
			mt := plugin.MetricType{
				Data_:      users[user][stat],
				Namespace_: core.NewNamespace("raintank", "apps", "gitstats", "user", user, stat),
				Timestamp_: collectionTime,
				Version_:   m.Version(),
			}
			metrics = append(metrics, mt)
		}
	}

	return metrics, nil
}

func userStats(ctx context.Context, user *github.User, client *GithubClient) (map[string]int, error) {

	stats := make(map[string]int)
	if user.PublicRepos != nil {
		stats["public_repos"] = *user.PublicRepos
	}
	if user.PublicGists != nil {
		stats["public_gists"] = *user.PublicGists
	}
	if user.Followers != nil {
		stats["followers"] = *user.Followers
	}
	if user.Following != nil {
		stats["following"] = *user.Following
	}

	if *user.Type == "Organization" {
		org, _, err := client.GetOrganizations(ctx, *user.Login)
		if err != nil {
			LogError("failed to lookup org data.", err)
			return nil, err
		}
		if org.PrivateGists != nil {
			stats["private_gists"] = *org.PrivateGists
		}
		if org.TotalPrivateRepos != nil {
			stats["private_repos"] = *org.TotalPrivateRepos
		}
		if org.DiskUsage != nil {
			stats["disk_usage"] = *org.DiskUsage
		}
	}
	fmt.Printf("\nstats: %v\n", stats)
	return stats, nil
}

func collectIssueMetrics(m plugin.MetricType, collectionTime time.Time, owner string, repo string, labels []*github.Label, issues []*github.Issue) ([]plugin.MetricType, error) {
	nolabel := "NoLabel"
	type labelMetric struct {
		label string
		state string
		value int
	}
	stats := make(map[string]*labelMetric, 0)

	for _, lv := range labels {
		labelSlug := slug.Make(*lv.Name)
		stats[fmt.Sprintf("%s.open", labelSlug)] = &labelMetric{labelSlug, "open", 0}
		stats[fmt.Sprintf("%s.closed", labelSlug)] = &labelMetric{labelSlug, "closed", 0}
	}
	stats[fmt.Sprintf("%s.open", nolabel)] = &labelMetric{nolabel, "open", 0}
	stats[fmt.Sprintf("%s.closed", nolabel)] = &labelMetric{nolabel, "closed", 0}

	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			stats[fmt.Sprintf("%s.%s", nolabel, *issue.State)].value++
		} else {
			for _, label := range issue.Labels {
				labelSlug := slug.Make(*label.Name)
				stats[fmt.Sprintf("%s.%s", labelSlug, *issue.State)].value++
			}
		}
	}

	metrics := make([]plugin.MetricType, 0)
	for k := range stats {
		mt := plugin.MetricType{
			Data_:      stats[k].value,
			Namespace_: core.NewNamespace("raintank", "apps", "gitstats", "repo", owner, repo, "issuesbylabel", stats[k].label, stats[k].state, "count"),
			Timestamp_: collectionTime,
			Version_:   m.Version(),
		}
		metrics = append(metrics, mt)
	}

	return metrics, nil
}

func repoStats(resp *github.Repository) (map[string]int, error) {
	stats := make(map[string]int)

	if resp.ForksCount != nil {
		stats["forks"] = *resp.ForksCount
	}
	if resp.OpenIssuesCount != nil {
		stats["issues"] = *resp.OpenIssuesCount
	}
	if resp.NetworkCount != nil {
		stats["network"] = *resp.NetworkCount
	}
	if resp.StargazersCount != nil {
		stats["stars"] = *resp.StargazersCount
	}
	if resp.SubscribersCount != nil {
		stats["subscribers"] = *resp.SubscribersCount
	}
	if resp.WatchersCount != nil {
		stats["watchers"] = *resp.WatchersCount
	}
	if resp.Size != nil {
		stats["size"] = *resp.Size
	}
	return stats, nil
}

//GetMetricTypes returns metric types for testing
func (f *Gitstats) GetMetricTypes(cfg plugin.ConfigType) ([]plugin.MetricType, error) {
	mts := make([]plugin.MetricType, 0)
	for _, metricName := range repoMetricNames {
		mts = append(mts, plugin.MetricType{
			Namespace_: core.NewNamespace("raintank", "apps", "gitstats", "repo").
				AddDynamicElement("owner", "repository owner").
				AddDynamicElement("repo", "repository name").
				AddStaticElement(metricName),
			Config_: cfg.ConfigDataNode,
		})
	}
	mts = append(mts, getIssuesByLabelTypes(cfg)...)

	for _, metricName := range userMetricNames {
		mts = append(mts, plugin.MetricType{
			Namespace_: core.NewNamespace("raintank", "apps", "gitstats", "user").
				AddDynamicElement("user", "user or orginisation name").
				AddStaticElement(metricName),
			Config_: cfg.ConfigDataNode,
		})
	}
	return mts, nil
}

func getIssuesByLabelTypes(cfg plugin.ConfigType) []plugin.MetricType {
	mts := []plugin.MetricType{}

	mts = append(mts, plugin.MetricType{
		Namespace_: core.NewNamespace("raintank", "apps", "gitstats", "repo").
			AddDynamicElement("owner", "repository owner").
			AddDynamicElement("repo", "repository name").
			AddStaticElement("issuesbylabel").
			AddDynamicElement("label", "issue label").
			AddDynamicElement("status", "issue status").
			AddStaticElement("count"),
		Config_: cfg.ConfigDataNode,
	})

	return mts
}

//GetConfigPolicy returns a ConfigPolicyTree for testing
func (f *Gitstats) GetConfigPolicy() (*cpolicy.ConfigPolicy, error) {
	c := cpolicy.New()
	rule, _ := cpolicy.NewStringRule("access_token", true)
	rule1, _ := cpolicy.NewStringRule("user", false, "")
	rule2, _ := cpolicy.NewStringRule("repo", false, "")

	p := cpolicy.NewPolicyNode()
	p.Add(rule)
	p.Add(rule1)
	p.Add(rule2)
	c.Add([]string{"raintank", "apps", "gitstats"}, p)
	return c, nil
}

//Meta returns meta data for testing
func Meta() *plugin.PluginMeta {
	return plugin.NewPluginMeta(
		Name,
		Version,
		Type,
		[]string{plugin.SnapGOBContentType},
		[]string{plugin.SnapGOBContentType},
		plugin.Unsecure(true),
		plugin.ConcurrencyCount(1000),
	)
}
