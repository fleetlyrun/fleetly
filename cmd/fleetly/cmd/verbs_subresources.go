package cmd

// 子资源动词组（B8，ADR-0096 决策 10 / design §4.7）：复合嵌套形态——
// `fleetly pubsubs topics …`（kafka 协议族）/`fleetly pubsubs streams …`（nats）
// /`fleetly buckets bucket …`（silo）。**避撞律记录**：`buckets` 复数组名已
// 被 Bucket Store 实体占用，Bucket 子资源组取单数 `bucket`（域包
// bucketresource/表 bucket_resources 同律分名）；pubsub 族子资源无撞名风险
// 直接 topics/streams。每子资源五动词：create/list/get/update/delete——
// Update 走 ConfirmableVerbs standard 档（--preview/--ticket/--yes 三形态，
// 铸票面 = PreviewUpdate*）；Delete 是 wide 档（删除即数据删除——铸票面 =
// PreviewDelete*，删除披露行先行）。

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// newPubSubTopicsGroup 是 `pubsubs topics` 嵌套组（kafka 协议族子资源面）。
func newPubSubTopicsGroup() commands.Command {
	return groupVerb("topics", "manage topics on a kafka-protocol pubsub instance (declarative rows; replicas are fixed at 1 on the single broker)",
		newTopicsCreateVerb(), newTopicsListVerb(), newTopicsGetVerb(), newTopicsUpdateVerb(), newTopicsDeleteVerb())
}

// newPubSubStreamsGroup 是 `pubsubs streams` 嵌套组（nats 子资源面）。
func newPubSubStreamsGroup() commands.Command {
	return groupVerb("streams", "manage JetStream streams on a nats pubsub instance (declarative rows; storage and retention are immutable after create)",
		newStreamsCreateVerb(), newStreamsListVerb(), newStreamsGetVerb(), newStreamsUpdateVerb(), newStreamsDeleteVerb())
}

// newBucketsBucketGroup 是 `buckets bucket` 嵌套组（silo 子资源面——单数
// 避撞：`buckets` 复数组名是 Bucket Store 实体组）。
func newBucketsBucketGroup() commands.Command {
	return groupVerb("bucket", "manage buckets on a bucket store instance (declarative rows with optional hard quotas)",
		newBucketCreateVerb(), newBucketListVerb(), newBucketGetVerb(), newBucketUpdateVerb(), newBucketDeleteVerb())
}

// —— pubsubs topics ——

func newTopicsCreateVerb() commands.Command {
	const name = "create"
	var pubsub, project string
	var idem idemKeyFlag
	var partitions int
	var retentionMs int64
	return &flaggedVerb{
		name:     name,
		synopsis: "Declare a topic on a pubsub instance (the convergence segment creates the live topic; replication factor is fixed at 1)",
		usage:    "pubsubs topics create --pubsub PUBSUB_ID [--project PROJECT] [--partitions N] [--retention-ms MS] [--idempotency-key K] NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			fs.IntVar(&partitions, "partitions", 1, "partition count (1 by default; can only increase later)")
			fs.Int64Var(&retentionMs, "retention-ms", 0, "retention.ms in milliseconds (0 = unmanaged, engine default)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if pubsub == "" {
				return usageErr(name, "--pubsub is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, err := resolvePubSubID(ctx, c, project, pubsub)
				if err != nil {
					return err
				}
				ctx = idem.bind(ctx)
				resp, err := c.PubSubs.CreateTopic(ctx, &structurev1.CreateTopicRequest{
					PubsubId: pubsubID, Name: args[0],
					Partitions: int32(partitions), RetentionMs: retentionMs, //nolint:gosec // 界检在下
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetTopic(), func() {
					t := resp.GetTopic()
					_, _ = fmt.Fprintf(env.Stdout,
						"declared topic %s (id %s) on pubsub %s\npartitions %d, retention %s\nthe convergence segment creates or matches the live topic (watch 'fleetly pubsubs topics get' until status is ready)\n",
						t.GetName(), t.GetId(), t.GetPubsubId(), t.GetPartitions(), describeRetentionMs(t.GetRetentionMs()))
				})
			})
		},
	}
}

func newTopicsListVerb() commands.Command {
	const name = "list"
	var pubsub, project string
	return &flaggedVerb{
		name:     name,
		synopsis: "List topics declared on a pubsub instance (oldest first)",
		usage:    "pubsubs topics list --pubsub PUBSUB_ID [--project PROJECT]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if pubsub == "" {
				return usageErr(name, "--pubsub is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, err := resolvePubSubID(ctx, c, project, pubsub)
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.ListTopics(ctx, &structurev1.ListTopicsRequest{PubsubId: pubsubID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "NAME\tID\tPARTITIONS\tRETENTION\tSTATUS\tCREATED")
					for _, t := range resp.GetTopics() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%d\t%s\t%s\t%s\n",
							t.GetName(), t.GetId(), t.GetPartitions(), describeRetentionMs(t.GetRetentionMs()), t.GetStatus(), t.GetCreatedAt())
					}
				})
			})
		},
	}
}

func newTopicsGetVerb() commands.Command {
	const name = "get"
	var pubsub, project string
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one topic declaration row (status carries the convergence verdict)",
		usage:    "pubsubs topics get --pubsub PUBSUB_ID [--project PROJECT] TOPIC_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one TOPIC_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, topicID, err := resolveTopicRef(ctx, c, project, pubsub, args[0])
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.GetTopic(ctx, &structurev1.GetTopicRequest{PubsubId: pubsubID, TopicId: topicID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetTopic(), func() {
					t := resp.GetTopic()
					_, _ = fmt.Fprintf(env.Stdout,
						"id %s\nname %s\npubsub %s\npartitions %d\nretention %s\nstatus %s\ncreated %s\n",
						t.GetId(), t.GetName(), t.GetPubsubId(), t.GetPartitions(), describeRetentionMs(t.GetRetentionMs()), t.GetStatus(), t.GetCreatedAt())
					if e := t.GetStatusError(); e != "" {
						_, _ = fmt.Fprintf(env.Stdout, "convergence error: %s\n", e)
					}
				})
			})
		},
	}
}

func newTopicsUpdateVerb() commands.Command {
	const name = "update"
	var pubsub, project string
	var partitions optionalInt
	var retentionMs optionalInt64
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Update a topic's desired state (partitions can only increase; no carrier restart — the sub-resource segment converges)",
		usage:    "pubsubs topics update --pubsub PUBSUB_ID [--project PROJECT] [--partitions N] [--retention-ms MS] [--yes | --preview | --ticket T] TOPIC_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			fs.Var(&partitions, "partitions", "new partition count (omitted = unchanged; can only increase)")
			fs.Var(&retentionMs, "retention-ms", "new retention.ms in milliseconds (omitted = unchanged; 0 = unmanaged)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one TOPIC_ID argument")
			}
			if !partitions.set && !retentionMs.set {
				return usageErr(name, "nothing to update: pass --partitions and/or --retention-ms")
			}
			plan, err := conf.resolve("pubsubs topics update")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, topicID, err := resolveTopicRef(ctx, c, project, pubsub, args[0])
				if err != nil {
					return err
				}
				req := &structurev1.UpdateTopicRequest{PubsubId: pubsubID, TopicId: topicID}
				if partitions.set {
					req.Partitions = int32Ptr(int32(partitions.value)) //nolint:gosec // CLI 界检在下
				}
				if retentionMs.set {
					req.RetentionMs = int64Ptr(retentionMs.value)
				}
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.PubSubs.PreviewUpdateTopic(ctx, &structurev1.PreviewUpdateTopicRequest{Update: req})
					if perr != nil {
						return perr
					}
					if rerr := renderSubUpdatePreview(env, jsonOut, "topic "+args[0], preview.GetChanges(), preview.GetTicket(), preview.GetTicketExpiresIn()); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Update topic")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.PubSubs.UpdateTopic(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					t := resp.GetTopic()
					_, _ = fmt.Fprintf(env.Stdout,
						"updated topic %s (id %s)\npartitions %d, retention %s\n%s\n",
						t.GetName(), t.GetId(), t.GetPartitions(), describeRetentionMs(t.GetRetentionMs()), resp.GetConvergedNote())
				})
			})
		},
	}
}

func newTopicsDeleteVerb() commands.Command {
	const name = "delete"
	var pubsub, project string
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Delete a topic (unrecoverable data destruction: the live topic and its partitioned data are removed; wide-tier confirmation)",
		usage:    "pubsubs topics delete --pubsub PUBSUB_ID [--project PROJECT] [--yes | --preview | --ticket T] TOPIC_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one TOPIC_ID argument")
			}
			plan, err := conf.resolve("pubsubs topics delete")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, topicID, err := resolveTopicRef(ctx, c, project, pubsub, args[0])
				if err != nil {
					return err
				}
				req := &structurev1.DeleteTopicRequest{PubsubId: pubsubID, TopicId: topicID}
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.PubSubs.PreviewDeleteTopic(ctx, &structurev1.PreviewDeleteTopicRequest{PubsubId: pubsubID, TopicId: topicID})
					if perr != nil {
						return perr
					}
					if rerr := renderSubDeletePreview(env, jsonOut, "topic", preview.GetTopic().GetName(),
						preview.GetDataBytes(), preview.GetDataMessages(), preview.GetDisclosure(), preview.GetTicket(), preview.GetTicketExpiresIn()); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Delete topic (data will be destroyed)")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.PubSubs.DeleteTopic(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"deleted topic %s (row tombstoned; the convergence segment removes the live topic and its data — unrecoverable)\n", args[0])
				})
			})
		},
	}
}

// —— pubsubs streams ——

func newStreamsCreateVerb() commands.Command {
	const name = "create"
	var pubsub, project, retention, storage string
	var idem idemKeyFlag
	var subjects string
	var maxStreamSize int64
	return &flaggedVerb{
		name:     name,
		synopsis: "Declare a JetStream stream on a nats pubsub instance (storage and retention are immutable after create)",
		usage:    "pubsubs streams create --pubsub PUBSUB_ID [--project PROJECT] [--subjects A,B] [--retention limits|interest|workqueue] [--storage file|memory] [--max-stream-size BYTES] [--idempotency-key K] NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			fs.StringVar(&subjects, "subjects", "", "comma-separated subject filters (e.g. \"orders.>,bills.>\")")
			fs.StringVar(&retention, "retention", "limits", "retention policy: limits | interest | workqueue (immutable after create)")
			fs.StringVar(&storage, "storage", "file", "storage backend: file | memory (immutable after create; memory streams cannot be snapshotted)")
			fs.Int64Var(&maxStreamSize, "max-stream-size", 0, "max stream size in bytes (0 = unlimited)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if pubsub == "" {
				return usageErr(name, "--pubsub is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, err := resolvePubSubID(ctx, c, project, pubsub)
				if err != nil {
					return err
				}
				ctx = idem.bind(ctx)
				resp, err := c.PubSubs.CreateStream(ctx, &structurev1.CreateStreamRequest{
					PubsubId: pubsubID, Name: args[0],
					Subjects: splitSubjects(subjects), Retention: retention, Storage: storage,
					MaxStreamSize: maxStreamSize,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetStream(), func() {
					s := resp.GetStream()
					_, _ = fmt.Fprintf(env.Stdout,
						"declared stream %s (id %s) on pubsub %s\nsubjects %s\nretention %s, storage %s, max size %s\nthe convergence segment creates or matches the live stream (watch 'fleetly pubsubs streams get' until status is ready)\n",
						s.GetName(), s.GetId(), s.GetPubsubId(), strings.Join(s.GetSubjects(), ","),
						s.GetRetention(), s.GetStorage(), describeSizeBytes(s.GetMaxStreamSize()))
				})
			})
		},
	}
}

func newStreamsListVerb() commands.Command {
	const name = "list"
	var pubsub, project string
	return &flaggedVerb{
		name:     name,
		synopsis: "List streams declared on a pubsub instance (oldest first)",
		usage:    "pubsubs streams list --pubsub PUBSUB_ID [--project PROJECT]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if pubsub == "" {
				return usageErr(name, "--pubsub is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, err := resolvePubSubID(ctx, c, project, pubsub)
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.ListStreams(ctx, &structurev1.ListStreamsRequest{PubsubId: pubsubID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "NAME\tID\tSUBJECTS\tRETENTION\tSTORAGE\tMAX_SIZE\tSTATUS\tCREATED")
					for _, s := range resp.GetStreams() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
							s.GetName(), s.GetId(), strings.Join(s.GetSubjects(), ","),
							s.GetRetention(), s.GetStorage(), describeSizeBytes(s.GetMaxStreamSize()), s.GetStatus(), s.GetCreatedAt())
					}
				})
			})
		},
	}
}

func newStreamsGetVerb() commands.Command {
	const name = "get"
	var pubsub, project string
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one stream declaration row",
		usage:    "pubsubs streams get --pubsub PUBSUB_ID [--project PROJECT] STREAM_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one STREAM_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, streamID, err := resolveStreamRef(ctx, c, project, pubsub, args[0])
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.GetStream(ctx, &structurev1.GetStreamRequest{PubsubId: pubsubID, StreamId: streamID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetStream(), func() {
					s := resp.GetStream()
					_, _ = fmt.Fprintf(env.Stdout,
						"id %s\nname %s\npubsub %s\nsubjects %s\nretention %s\nstorage %s\nmax size %s\nstatus %s\ncreated %s\n",
						s.GetId(), s.GetName(), s.GetPubsubId(), strings.Join(s.GetSubjects(), ","),
						s.GetRetention(), s.GetStorage(), describeSizeBytes(s.GetMaxStreamSize()), s.GetStatus(), s.GetCreatedAt())
					if e := s.GetStatusError(); e != "" {
						_, _ = fmt.Fprintf(env.Stdout, "convergence error: %s\n", e)
					}
				})
			})
		},
	}
}

func newStreamsUpdateVerb() commands.Command {
	const name = "update"
	var pubsub, project, subjects string
	var subjectsSet bool
	var maxStreamSize optionalInt64
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Update a stream's mutable axes (subjects and max size; retention/storage are immutable)",
		usage:    "pubsubs streams update --pubsub PUBSUB_ID [--project PROJECT] [--subjects A,B] [--max-stream-size BYTES] [--yes | --preview | --ticket T] STREAM_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			fs.StringVar(&subjects, "subjects", "", "comma-separated subject filters (full replacement; empty = clear all)")
			fs.BoolVar(&subjectsSet, "subjects-set", false, "mark that --subjects is intentionally provided (proto3 cannot distinguish an empty set; the flag is the presence bit)")
			fs.Var(&maxStreamSize, "max-stream-size", "max stream size in bytes (omitted = unchanged; 0 = unlimited)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one STREAM_ID argument")
			}
			if !subjectsSet && !maxStreamSize.set {
				return usageErr(name, "nothing to update: pass --subjects (with --subjects-set) and/or --max-stream-size")
			}
			plan, err := conf.resolve("pubsubs streams update")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, streamID, err := resolveStreamRef(ctx, c, project, pubsub, args[0])
				if err != nil {
					return err
				}
				req := &structurev1.UpdateStreamRequest{PubsubId: pubsubID, StreamId: streamID}
				if subjectsSet {
					req.Subjects = splitSubjects(subjects)
				}
				if maxStreamSize.set {
					req.MaxStreamSize = int64Ptr(maxStreamSize.value)
				}
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.PubSubs.PreviewUpdateStream(ctx, &structurev1.PreviewUpdateStreamRequest{Update: req})
					if perr != nil {
						return perr
					}
					if rerr := renderSubUpdatePreview(env, jsonOut, "stream "+args[0], preview.GetChanges(), preview.GetTicket(), preview.GetTicketExpiresIn()); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Update stream")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.PubSubs.UpdateStream(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					s := resp.GetStream()
					_, _ = fmt.Fprintf(env.Stdout,
						"updated stream %s (id %s)\nsubjects %s, max size %s\n%s\n",
						s.GetName(), s.GetId(), strings.Join(s.GetSubjects(), ","), describeSizeBytes(s.GetMaxStreamSize()), resp.GetConvergedNote())
				})
			})
		},
	}
}

func newStreamsDeleteVerb() commands.Command {
	const name = "delete"
	var pubsub, project string
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Delete a stream (unrecoverable data destruction: retained messages are removed; wide-tier confirmation)",
		usage:    "pubsubs streams delete --pubsub PUBSUB_ID [--project PROJECT] [--yes | --preview | --ticket T] STREAM_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsub, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one STREAM_ID argument")
			}
			plan, err := conf.resolve("pubsubs streams delete")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				pubsubID, streamID, err := resolveStreamRef(ctx, c, project, pubsub, args[0])
				if err != nil {
					return err
				}
				req := &structurev1.DeleteStreamRequest{PubsubId: pubsubID, StreamId: streamID}
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.PubSubs.PreviewDeleteStream(ctx, &structurev1.PreviewDeleteStreamRequest{PubsubId: pubsubID, StreamId: streamID})
					if perr != nil {
						return perr
					}
					if rerr := renderSubDeletePreview(env, jsonOut, "stream", preview.GetStream().GetName(),
						preview.GetDataBytes(), preview.GetDataMessages(), preview.GetDisclosure(), preview.GetTicket(), preview.GetTicketExpiresIn()); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Delete stream (data will be destroyed)")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.PubSubs.DeleteStream(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"deleted stream %s (row tombstoned; the convergence segment removes the live stream and its messages — unrecoverable)\n", args[0])
				})
			})
		},
	}
}

// —— buckets bucket ——

func newBucketCreateVerb() commands.Command {
	const name = "create"
	var store, project string
	var idem idemKeyFlag
	var quotaBytes int64
	return &flaggedVerb{
		name:     name,
		synopsis: "Declare a bucket on a bucket store instance (optional hard quota)",
		usage:    "buckets bucket create --store BUCKET_STORE_ID [--project PROJECT] [--quota-bytes N] [--idempotency-key K] NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&store, "store", "", "owning bucket store id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
			fs.Int64Var(&quotaBytes, "quota-bytes", 0, "hard quota in bytes (0 = no quota)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if store == "" {
				return usageErr(name, "--store is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				storeID, err := resolveBucketStoreID(ctx, c, project, store)
				if err != nil {
					return err
				}
				ctx = idem.bind(ctx)
				resp, err := c.BucketStores.CreateBucket(ctx, &structurev1.CreateBucketRequest{
					BucketStoreId: storeID, Name: args[0], QuotaBytes: quotaBytes,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetBucket(), func() {
					b := resp.GetBucket()
					_, _ = fmt.Fprintf(env.Stdout,
						"declared bucket %s (id %s) on bucket store %s\nquota %s\nthe convergence segment creates or matches the live bucket (watch 'fleetly buckets bucket get' until status is ready)\n",
						b.GetName(), b.GetId(), b.GetBucketStoreId(), describeSizeBytes(b.GetQuotaBytes()))
				})
			})
		},
	}
}

func newBucketListVerb() commands.Command {
	const name = "list"
	var store, project string
	return &flaggedVerb{
		name:     name,
		synopsis: "List buckets declared on a bucket store instance (oldest first)",
		usage:    "buckets bucket list --store BUCKET_STORE_ID [--project PROJECT]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&store, "store", "", "owning bucket store id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if store == "" {
				return usageErr(name, "--store is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				storeID, err := resolveBucketStoreID(ctx, c, project, store)
				if err != nil {
					return err
				}
				resp, err := c.BucketStores.ListBuckets(ctx, &structurev1.ListBucketsRequest{BucketStoreId: storeID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "NAME\tID\tQUOTA\tSTATUS\tCREATED")
					for _, b := range resp.GetBuckets() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\n",
							b.GetName(), b.GetId(), describeSizeBytes(b.GetQuotaBytes()), b.GetStatus(), b.GetCreatedAt())
					}
				})
			})
		},
	}
}

func newBucketGetVerb() commands.Command {
	const name = "get"
	var store, project string
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one bucket declaration row",
		usage:    "buckets bucket get --store BUCKET_STORE_ID [--project PROJECT] BUCKET_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&store, "store", "", "owning bucket store id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BUCKET_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				storeID, bucketID, err := resolveBucketSubRef(ctx, c, project, store, args[0])
				if err != nil {
					return err
				}
				resp, err := c.BucketStores.GetBucket(ctx, &structurev1.GetBucketRequest{BucketStoreId: storeID, BucketId: bucketID})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetBucket(), func() {
					b := resp.GetBucket()
					_, _ = fmt.Fprintf(env.Stdout,
						"id %s\nname %s\nbucket store %s\nquota %s\nstatus %s\ncreated %s\n",
						b.GetId(), b.GetName(), b.GetBucketStoreId(), describeSizeBytes(b.GetQuotaBytes()), b.GetStatus(), b.GetCreatedAt())
					if e := b.GetStatusError(); e != "" {
						_, _ = fmt.Fprintf(env.Stdout, "convergence error: %s\n", e)
					}
				})
			})
		},
	}
}

func newBucketUpdateVerb() commands.Command {
	const name = "update"
	var store, project string
	var quotaBytes optionalInt64
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Update a bucket's desired quota",
		usage:    "buckets bucket update --store BUCKET_STORE_ID [--project PROJECT] [--quota-bytes N] [--yes | --preview | --ticket T] BUCKET_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&store, "store", "", "owning bucket store id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
			fs.Var(&quotaBytes, "quota-bytes", "hard quota in bytes (omitted = unchanged; 0 = no quota)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BUCKET_ID argument")
			}
			if !quotaBytes.set {
				return usageErr(name, "nothing to update: pass --quota-bytes")
			}
			plan, err := conf.resolve("buckets bucket update")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				storeID, bucketID, err := resolveBucketSubRef(ctx, c, project, store, args[0])
				if err != nil {
					return err
				}
				req := &structurev1.UpdateBucketRequest{BucketStoreId: storeID, BucketId: bucketID, QuotaBytes: int64Ptr(quotaBytes.value)}
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.BucketStores.PreviewUpdateBucket(ctx, &structurev1.PreviewUpdateBucketRequest{Update: req})
					if perr != nil {
						return perr
					}
					if rerr := renderSubUpdatePreview(env, jsonOut, "bucket "+args[0], preview.GetChanges(), preview.GetTicket(), preview.GetTicketExpiresIn()); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Update bucket")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.BucketStores.UpdateBucket(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					b := resp.GetBucket()
					_, _ = fmt.Fprintf(env.Stdout,
						"updated bucket %s (id %s)\nquota %s\n%s\n",
						b.GetName(), b.GetId(), describeSizeBytes(b.GetQuotaBytes()), resp.GetConvergedNote())
				})
			})
		},
	}
}

func newBucketDeleteVerb() commands.Command {
	const name = "delete"
	var store, project string
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Delete a bucket (unrecoverable data destruction: every object in it is force-removed; wide-tier confirmation)",
		usage:    "buckets bucket delete --store BUCKET_STORE_ID [--project PROJECT] [--yes | --preview | --ticket T] BUCKET_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&store, "store", "", "owning bucket store id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BUCKET_ID argument")
			}
			plan, err := conf.resolve("buckets bucket delete")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				storeID, bucketID, err := resolveBucketSubRef(ctx, c, project, store, args[0])
				if err != nil {
					return err
				}
				req := &structurev1.DeleteBucketRequest{BucketStoreId: storeID, BucketId: bucketID}
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.BucketStores.PreviewDeleteBucket(ctx, &structurev1.PreviewDeleteBucketRequest{BucketStoreId: storeID, BucketId: bucketID})
					if perr != nil {
						return perr
					}
					if rerr := renderSubDeletePreview(env, jsonOut, "bucket", preview.GetBucket().GetName(),
						preview.GetDataBytes(), preview.GetDataObjects(), preview.GetDisclosure(), preview.GetTicket(), preview.GetTicketExpiresIn()); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Delete bucket (data will be destroyed)")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.BucketStores.DeleteBucket(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"deleted bucket %s (row tombstoned; the convergence segment force-removes the live bucket and its objects — unrecoverable)\n", args[0])
				})
			})
		},
	}
}

// —— 子资源共享小帮手 ——

// resolveTopicRef 解析 (pubsub, topic) 引用（topic 无 Resolve RPC：id 直通
// /name 经 ListTopics 父实例内匹配）。
func resolveTopicRef(ctx context.Context, c *fleetly.Client, projectFlag, pubsubFlag, ref string) (string, string, error) {
	pubsubID, err := resolvePubSubID(ctx, c, projectFlag, pubsubFlag)
	if err != nil {
		return "", "", err
	}
	if mintedIDRe.MatchString(ref) {
		return pubsubID, ref, nil
	}
	list, err := c.PubSubs.ListTopics(ctx, &structurev1.ListTopicsRequest{PubsubId: pubsubID})
	if err != nil {
		return "", "", err
	}
	for _, t := range list.GetTopics() {
		if t.GetName() == ref {
			return pubsubID, t.GetId(), nil
		}
	}
	return "", "", fmt.Errorf("topic %q not found on pubsub %s", ref, pubsubFlag)
}

// resolveStreamRef 同形（streams）。
func resolveStreamRef(ctx context.Context, c *fleetly.Client, projectFlag, pubsubFlag, ref string) (string, string, error) {
	pubsubID, err := resolvePubSubID(ctx, c, projectFlag, pubsubFlag)
	if err != nil {
		return "", "", err
	}
	if mintedIDRe.MatchString(ref) {
		return pubsubID, ref, nil
	}
	list, err := c.PubSubs.ListStreams(ctx, &structurev1.ListStreamsRequest{PubsubId: pubsubID})
	if err != nil {
		return "", "", err
	}
	for _, s := range list.GetStreams() {
		if s.GetName() == ref {
			return pubsubID, s.GetId(), nil
		}
	}
	return "", "", fmt.Errorf("stream %q not found on pubsub %s", ref, pubsubFlag)
}

// resolveBucketSubRef 同形（buckets）。
func resolveBucketSubRef(ctx context.Context, c *fleetly.Client, projectFlag, storeFlag, ref string) (string, string, error) {
	storeID, err := resolveBucketStoreID(ctx, c, projectFlag, storeFlag)
	if err != nil {
		return "", "", err
	}
	if mintedIDRe.MatchString(ref) {
		return storeID, ref, nil
	}
	list, err := c.BucketStores.ListBuckets(ctx, &structurev1.ListBucketsRequest{BucketStoreId: storeID})
	if err != nil {
		return "", "", err
	}
	for _, b := range list.GetBuckets() {
		if b.GetName() == ref {
			return storeID, b.GetId(), nil
		}
	}
	return "", "", fmt.Errorf("bucket %q not found on bucket store %s", ref, storeFlag)
}

// splitSubjects 拆分逗号分隔的 subjects 旗标值。
func splitSubjects(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// describeRetentionMs 渲染 retention 面（0 = unmanaged）。
func describeRetentionMs(ms int64) string {
	if ms == 0 {
		return "unmanaged (engine default)"
	}
	return fmt.Sprintf("%dms", ms)
}

// describeSizeBytes 渲染字节面（0 = 无上限/无配额语义随列）。
func describeSizeBytes(v int64) string {
	if v == 0 {
		return "none"
	}
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%dB", v)
	}
	div, exp := int64(unit), 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(v)/float64(div), "KMGTPE"[exp])
}

// renderSubUpdatePreview 渲染子资源 Update 预演（三面共用体：diff 行 +
// ticket）。
func renderSubUpdatePreview(env *commands.Environment, jsonOut bool, target string, changes []*structurev1.SubResourceChange, ticket string, expiresIn int32) error {
	if jsonOut {
		out := &structurev1.PreviewUpdateTopicResponse{Changes: changes, Ticket: ticket, TicketExpiresIn: expiresIn}
		return writeProtoJSON(env.Stdout, out)
	}
	lines := []string{fmt.Sprintf("update preview for %s", target)}
	if len(changes) == 0 {
		lines = append(lines, "  no changes (the request matches the current desired state)")
	}
	for _, ch := range changes {
		lines = append(lines, fmt.Sprintf("  %s: %s -> %s", ch.GetKey(), ch.GetFrom(), ch.GetTo()))
	}
	lines = append(lines,
		"consequences:",
		"  the sub-resource convergence segment ensures the live object (no carrier restart)",
		fmt.Sprintf("ticket: %s (expires in %ds)", ticket, expiresIn))
	_, err := fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
	return err
}

// renderSubDeletePreview 渲染子资源删除披露行（wide 档：Consequences 先行
// ——live 数据量可得则列，kafka 面如实缺席）。
func renderSubDeletePreview(env *commands.Environment, jsonOut bool, kind, name string, dataBytes, dataMessages int64, disclosure, ticket string, expiresIn int32) error {
	if jsonOut {
		out := &structurev1.PreviewDeleteTopicResponse{
			DataBytes: dataBytes, DataMessages: dataMessages,
			Disclosure: disclosure, Ticket: ticket, TicketExpiresIn: expiresIn,
		}
		return writeProtoJSON(env.Stdout, out)
	}
	lines := []string{fmt.Sprintf("delete preview for %s %s", kind, name)}
	lines = append(lines, "  "+disclosure)
	lines = append(lines, fmt.Sprintf("ticket: %s (expires in %ds)", ticket, expiresIn))
	_, err := fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
	return err
}

// int64Ptr / int32Ptr 是 proto optional 标量指针帮手。
func int64Ptr(v int64) *int64 { return &v }
func int32Ptr(v int32) *int32 { return &v }

// optionalInt64 是 optionalInt 的 int64 形（retention/quota/max-size 的
// 补丁语义轴）。
type optionalInt64 struct {
	value int64
	set   bool
}

func (o *optionalInt64) String() string {
	if o == nil || !o.set {
		return ""
	}
	return strconv.FormatInt(o.value, 10)
}

func (o *optionalInt64) Set(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("expected an integer (got %q)", v)
	}
	o.value, o.set = n, true
	return nil
}
