// Copyright 2022 The LUCI Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"regexp"

	luciproto "go.chromium.org/luci/common/proto"
	"go.chromium.org/luci/config/validation"
	"google.golang.org/protobuf/types/known/durationpb"

	"go.chromium.org/luci/analysis/internal/analysis/metrics"
	"go.chromium.org/luci/analysis/internal/clustering/algorithms/testname/rules"
	"go.chromium.org/luci/analysis/pbutil"
	configpb "go.chromium.org/luci/analysis/proto/config"
)

const maxHysteresisPercent = 1000

var (
	// https://cloud.google.com/storage/docs/naming-buckets
	bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9\-_.]{1,220}[a-z0-9]$`)

	// From https://source.chromium.org/chromium/infra/infra/+/main:appengine/monorail/project/project_constants.py;l=13.
	monorailProjectRE = regexp.MustCompile(`^[a-z0-9][-a-z0-9]{0,61}[a-z0-9]$`)

	// https://source.chromium.org/chromium/infra/infra/+/main:luci/appengine/auth_service/proto/realms_config.proto;l=85;drc=04e290f764a293d642d287b0118e9880df4afb35
	realmRE = regexp.MustCompile(`^[a-z0-9_\.\-/]{1,400}$`)

	// Matches valid prefixes to use when displaying bugs.
	// E.g. "crbug.com", "fxbug.dev".
	prefixRE = regexp.MustCompile(`^[a-z0-9\-.]{0,64}$`)

	// hostnameRE excludes most invalid hostnames.
	hostnameRE = regexp.MustCompile(`^[a-z][a-z9-9\-.]{0,62}[a-z]$`)

	// nameRE matches valid rule names.
	ruleNameRE = regexp.MustCompile(`^[a-zA-Z0-9\-(), ]+$`)

	// anyRE matches any input.
	anyRE = regexp.MustCompile(`^.*$`)

	// Patterns for BigQuery table.
	// https://cloud.google.com/resource-manager/docs/creating-managing-projects
	cloudProjectRE = regexp.MustCompile(`^[a-z][a-z0-9\-]{4,28}[a-z0-9]$`)
	// https://cloud.google.com/bigquery/docs/datasets#dataset-naming
	datasetRE = regexp.MustCompile(`^[a-zA-Z0-9_]*$`)
	// https://cloud.google.com/bigquery/docs/tables#table_naming
	tableRE = regexp.MustCompile(`^[\p{L}\p{M}\p{N}\p{Pc}\p{Pd}\p{Zs}]*$`)
)

func validateConfig(ctx *validation.Context, cfg *configpb.Config) {
	validateHostname(ctx, "monorail_hostname", cfg.MonorailHostname, false /*optional*/)
	validateStringConfig(ctx, "chunk_gcs_bucket", cfg.ChunkGcsBucket, bucketRE)
	// Limit to default max_concurrent_requests of 1000.
	// https://cloud.google.com/appengine/docs/standard/go111/config/queueref
	validateIntegerConfig(ctx, "reclustering_workers", cfg.ReclusteringWorkers, 1000)
	// Limit within GAE autoscaling request timeout of 10 minutes.
	// https://cloud.google.com/appengine/docs/standard/python/how-instances-are-managed
	validateIntegerConfig(ctx, "reclustering_interval_minutes", cfg.ReclusteringIntervalMinutes, 9)
}

func validateHostname(ctx *validation.Context, name, hostname string, optional bool) {
	ctx.Enter(name)
	if hostname == "" {
		if !optional {
			ctx.Errorf("empty value is not allowed")
		}
	} else if !hostnameRE.MatchString(hostname) {
		ctx.Errorf("invalid hostname: %q", hostname)
	}
	ctx.Exit()
}

func validateStringConfig(ctx *validation.Context, name, cfg string, re *regexp.Regexp) {
	ctx.Enter(name)
	switch err := pbutil.ValidateWithRe(re, cfg); err {
	case pbutil.Unspecified:
		ctx.Errorf("empty %s is not allowed", name)
	case pbutil.DoesNotMatch:
		ctx.Errorf("invalid %s: %q", name, cfg)
	}
	ctx.Exit()
}

func validateIntegerConfig(ctx *validation.Context, name string, cfg, max int64) {
	ctx.Enter(name)
	defer ctx.Exit()

	if cfg < 0 {
		ctx.Errorf("value is less than zero")
	}
	if cfg >= max {
		ctx.Errorf("value is greater than %v", max)
	}
}

func validateDuration(ctx *validation.Context, name string, du *durationpb.Duration) {
	ctx.Enter(name)
	defer ctx.Exit()

	switch {
	case du == nil:
		ctx.Errorf("empty %s is not allowed", name)
	case du.CheckValid() != nil:
		ctx.Errorf("%s is invalid", name)
	case du.AsDuration() < 0:
		ctx.Errorf("%s is less than 0", name)
	}
}

func validateUpdateTestVariantTask(ctx *validation.Context, utCfg *configpb.UpdateTestVariantTask) {
	ctx.Enter("update_test_variant")
	defer ctx.Exit()
	if utCfg == nil {
		return
	}
	validateDuration(ctx, "interval", utCfg.UpdateTestVariantTaskInterval)
	validateDuration(ctx, "duration", utCfg.TestVariantStatusUpdateDuration)
}

func validateBigQueryTable(ctx *validation.Context, tCfg *configpb.BigQueryExport_BigQueryTable) {
	ctx.Enter("table")
	defer ctx.Exit()
	if tCfg == nil {
		ctx.Errorf("empty bigquery table is not allowed")
		return
	}
	validateStringConfig(ctx, "cloud_project", tCfg.CloudProject, cloudProjectRE)
	validateStringConfig(ctx, "dataset", tCfg.Dataset, datasetRE)
	validateStringConfig(ctx, "table_name", tCfg.Table, tableRE)
}

func validateBigQueryExport(ctx *validation.Context, bqCfg *configpb.BigQueryExport) {
	ctx.Enter("bigquery_export")
	defer ctx.Exit()
	if bqCfg == nil {
		return
	}
	validateBigQueryTable(ctx, bqCfg.Table)
	if bqCfg.GetPredicate() == nil {
		return
	}
	if err := pbutil.ValidateAnalyzedTestVariantPredicate(bqCfg.Predicate); err != nil {
		ctx.Errorf(fmt.Sprintf("%s", err))
	}
}

func validateTestVariantAnalysisConfig(ctx *validation.Context, tvCfg *configpb.TestVariantAnalysisConfig) {
	ctx.Enter("test_variant")
	defer ctx.Exit()
	if tvCfg == nil {
		return
	}
	validateUpdateTestVariantTask(ctx, tvCfg.UpdateTestVariantTask)
	for _, bqe := range tvCfg.BqExports {
		validateBigQueryExport(ctx, bqe)
	}
}

func validateRealmConfig(ctx *validation.Context, rCfg *configpb.RealmConfig) {
	ctx.Enter(fmt.Sprintf("realm %s", rCfg.Name))
	defer ctx.Exit()

	validateStringConfig(ctx, "realm_name", rCfg.Name, realmRE)
	validateTestVariantAnalysisConfig(ctx, rCfg.TestVariantAnalysis)
}

// validateProjectConfigRaw deserializes the project-level config message
// and passes it through the validator.
func validateProjectConfigRaw(ctx *validation.Context, content string) *configpb.ProjectConfig {
	msg := &configpb.ProjectConfig{}
	if err := luciproto.UnmarshalTextML(content, msg); err != nil {
		ctx.Errorf("failed to unmarshal as text proto: %s", err)
		return nil
	}
	ValidateProjectConfig(ctx, msg)
	return msg
}

func ValidateProjectConfig(ctx *validation.Context, cfg *configpb.ProjectConfig) {
	if cfg.BugSystem == configpb.ProjectConfig_MONORAIL && cfg.Monorail == nil {
		ctx.Errorf("monorail configuration is required when the configured bug system is Monorail")
		return
	}

	if cfg.BugSystem == configpb.ProjectConfig_BUGANIZER && cfg.Buganizer == nil {
		ctx.Errorf("buganizer configuration is required when the configured bug system is Buganizer")
		return
	}

	if cfg.Monorail != nil {
		validateMonorail(ctx, cfg.Monorail, cfg.BugFilingThresholds)
	}
	if cfg.Buganizer != nil {
		validateBuganizer(ctx, cfg.Buganizer, cfg.BugFilingThresholds)
	}
	// Validate BugFilingThreshold when it is not nil or there is a bug system specified.
	if cfg.BugFilingThresholds != nil || cfg.BugSystem != configpb.ProjectConfig_BUG_SYSTEM_UNSPECIFIED {
		validateImpactMetricThresholds(ctx, cfg.BugFilingThresholds, "bug_filing_thresholds")
	}
	for _, rCfg := range cfg.Realms {
		validateRealmConfig(ctx, rCfg)
	}
	validateClustering(ctx, cfg.Clustering)
}

func validateBuganizer(ctx *validation.Context, cfg *configpb.BuganizerProject, bugFilingThres []*configpb.ImpactMetricThreshold) {
	ctx.Enter("buganizer")

	defer ctx.Exit()

	if cfg == nil {
		ctx.Errorf("buganizer must be specified")
		return
	}
	validateBuganizerDefaultComponent(ctx, cfg.DefaultComponent)
	validatePriorityHysteresisPercent(ctx, cfg.PriorityHysteresisPercent)
	validateBuganizerPriorityMappings(ctx, cfg.PriorityMappings, bugFilingThres)
}

func validateBuganizerDefaultComponent(ctx *validation.Context, component *configpb.BuganizerComponent) {
	ctx.Enter("default_component")
	defer ctx.Exit()
	if component == nil {
		ctx.Errorf("default component must be specified")
		return
	}
	if component.Id <= 0 {
		ctx.Errorf("invalid buganizer default component id: %d", component.Id)
	}
}

func validateBuganizerPriorityMappings(ctx *validation.Context, mappings []*configpb.BuganizerProject_PriorityMapping, bugFilingThres []*configpb.ImpactMetricThreshold) {
	ctx.Enter("priority_mappings")
	defer ctx.Exit()
	if mappings == nil {
		ctx.Errorf("priority_mappings must be specified")
		return
	}
	if len(mappings) == 0 {
		ctx.Errorf("at least one buganizer priority mapping must be specified")
	}

	for i, mapping := range mappings {
		validateBuganizerPriorityMapping(ctx, i, mapping, bugFilingThres)
		if i == len(mappings)-1 {
			// The lowest priority threshold must be satisfied by
			// the bug-filing threshold. This ensures that bugs meeting the
			// bug-filing threshold meet the bug keep-open threshold.
			validatePrioritySatisfiedByBugFilingThreshold(ctx, mapping.Thresholds, bugFilingThres)
		}
	}

	// Validate priorites are in decending order
	for i := len(mappings) - 1; i >= 1; i-- {
		ctx.Enter("[%v]", i)
		for j := i - 1; j >= 0; j-- {
			if mappings[j].Priority > mappings[i].Priority {
				ctx.Errorf("invalid priority_mappings order, must be in decending order, found: %s before %s",
					mappings[i].Priority.String(),
					mappings[j].Priority.String())
				break
			}
		}
		ctx.Exit()
	}
}

func validateBuganizerPriorityMapping(ctx *validation.Context, index int, mapping *configpb.BuganizerProject_PriorityMapping, bugFilingThres []*configpb.ImpactMetricThreshold) {
	ctx.Enter("[%v]", index)
	defer ctx.Exit()
	validateBuganizerPriority(ctx, mapping.Priority)
	validateImpactMetricThresholds(ctx, mapping.Thresholds, "thresholds")
}

func validateBuganizerPriority(ctx *validation.Context, priority configpb.BuganizerPriority) {
	ctx.Enter("priority")
	defer ctx.Exit()
	if priority <= 0 || priority > configpb.BuganizerPriority_P4 {
		ctx.Errorf("invalid priority: %s", priority.String())
		return
	}
}

func validateMonorail(ctx *validation.Context, cfg *configpb.MonorailProject, bugFilingThres []*configpb.ImpactMetricThreshold) {
	ctx.Enter("monorail")
	defer ctx.Exit()

	if cfg == nil {
		ctx.Errorf("monorail must be specified")
		return
	}

	validateStringConfig(ctx, "project", cfg.Project, monorailProjectRE)
	validateDefaultFieldValues(ctx, cfg.DefaultFieldValues)
	validateFieldID(ctx, cfg.PriorityFieldId, "priority_field_id")
	validateMonorailPriorities(ctx, cfg.Priorities, bugFilingThres)
	validatePriorityHysteresisPercent(ctx, cfg.PriorityHysteresisPercent)
	validateDisplayPrefix(ctx, cfg.DisplayPrefix)
	validateHostname(ctx, "monorail_hostname", cfg.MonorailHostname, true /*optional*/)
}

func validateDefaultFieldValues(ctx *validation.Context, fvs []*configpb.MonorailFieldValue) {
	ctx.Enter("default_field_values")
	for i, fv := range fvs {
		ctx.Enter("[%v]", i)
		validateFieldValue(ctx, fv)
		ctx.Exit()
	}
	ctx.Exit()
}

func validateFieldID(ctx *validation.Context, fieldID int64, fieldName string) {
	ctx.Enter(fieldName)
	if fieldID < 0 {
		ctx.Errorf("value must be non-negative")
	}
	ctx.Exit()
}

func validateFieldValue(ctx *validation.Context, fv *configpb.MonorailFieldValue) {
	validateFieldID(ctx, fv.GetFieldId(), "field_id")
	// No validation applies to field value.
}

func validateMonorailPriorities(ctx *validation.Context, ps []*configpb.MonorailPriority, bugFilingThres []*configpb.ImpactMetricThreshold) {
	ctx.Enter("priorities")
	if len(ps) == 0 {
		ctx.Errorf("at least one monorail priority must be specified")
	}
	for i, priority := range ps {
		ctx.Enter("[%v]", i)
		validateMonorailPriority(ctx, priority)
		if i == len(ps)-1 {
			// The lowest priority threshold must be satisfied by
			// the bug-filing threshold. This ensures that bugs meeting the
			// bug-filing threshold meet the bug keep-open threshold.
			validatePrioritySatisfiedByBugFilingThreshold(ctx, priority.Thresholds, bugFilingThres)
		}
		ctx.Exit()
	}
	ctx.Exit()
}

func validateMonorailPriority(ctx *validation.Context, p *configpb.MonorailPriority) {
	validatePriorityValue(ctx, p.Priority)
	validateImpactMetricThresholds(ctx, p.Thresholds, "thresholds")
}

func validatePrioritySatisfiedByBugFilingThreshold(ctx *validation.Context, priorityThreshold, bugFilingThres []*configpb.ImpactMetricThreshold) {
	ctx.Enter("threshold")
	defer ctx.Exit()
	if len(priorityThreshold) == 0 || len(bugFilingThres) == 0 {
		// Priority without threshold and no bug filing threshold specified
		// are already reported as errors elsewhere.
		return
	}
	// Check if all condition in the bug filing threshold satisfy the priority threshold.
	for i, t := range bugFilingThres {
		ctx.Enter("[%v]", i)
		validateBugFilingThresholdSatisfiesMetricThresold(ctx, pbutil.MetricThresholdByID(t.MetricId, priorityThreshold), t.Threshold, t.MetricId)
		ctx.Exit()
	}
}

func validatePriorityValue(ctx *validation.Context, value string) {
	ctx.Enter("priority")
	// Although it is possible to allow the priority field to be empty, it
	// would be rather unusual for a project to set itself up this way. For
	// now, prefer to enforce priority values are non-empty as this will pick
	// likely configuration errors.
	if value == "" {
		ctx.Errorf("empty value is not allowed")
	}
	ctx.Exit()
}

func validateImpactMetricThresholds(ctx *validation.Context, ts []*configpb.ImpactMetricThreshold, fieldName string) {
	ctx.Enter(fieldName)
	defer ctx.Exit()

	if len(ts) == 0 {
		ctx.Errorf("impact thresholds must be specified")
	}
	seen := map[string]bool{}
	for i, t := range ts {
		ctx.Enter("[%v]", i)
		if _, err := metrics.ByID(metrics.ID(t.MetricId)); err != nil {
			ctx.Error(err)
		}
		if _, ok := seen[t.MetricId]; ok {
			ctx.Errorf("same metric can't have more than one threshold")
		}
		seen[t.MetricId] = true
		validateMetricThreshold(ctx, t.Threshold, "threshold")
		ctx.Exit()
	}
}

func validateMetricThreshold(ctx *validation.Context, t *configpb.MetricThreshold, fieldName string) {
	ctx.Enter(fieldName)
	defer ctx.Exit()

	if t == nil {
		// Not specified.
		return
	}

	validateThresholdValue(ctx, t.OneDay, "one_day")
	validateThresholdValue(ctx, t.ThreeDay, "three_day")
	validateThresholdValue(ctx, t.SevenDay, "seven_day")
}

func validatePriorityHysteresisPercent(ctx *validation.Context, value int64) {
	ctx.Enter("priority_hysteresis_percent")
	if value > maxHysteresisPercent {
		ctx.Errorf("value must not exceed %v percent", maxHysteresisPercent)
	}
	if value < 0 {
		ctx.Errorf("value must not be negative")
	}
	ctx.Exit()
}

func validateThresholdValue(ctx *validation.Context, value *int64, fieldName string) {
	ctx.Enter(fieldName)
	if value != nil && *value < 0 {
		ctx.Errorf("value must be non-negative")
	}
	if value != nil && *value >= 1000*1000 {
		ctx.Errorf("value must be less than one million")
	}
	ctx.Exit()
}

func validateBugFilingThresholdSatisfiesMetricThresold(ctx *validation.Context, threshold *configpb.MetricThreshold, bugFilingThres *configpb.MetricThreshold, fieldName string) {
	ctx.Enter(fieldName)
	defer ctx.Exit()
	if threshold == nil {
		threshold = &configpb.MetricThreshold{}
	}
	if bugFilingThres == nil {
		// Bugs are not filed based on this metric. So
		// we do not need to check that bugs filed
		// based on this metric will stay open.
		return
	}
	// If the bug-filing threshold is:
	//  - Presubmit Runs Failed (1-day) > 3
	// And the keep-open threshold is:
	//  - Presubmit Runs Failed (7-day) > 1
	// The former actually implies the latter, even though the time periods
	// are different. Reflect that in the validation here, by calculating
	// the effective thresholds for one and three days that are sufficient
	// to keep a bug open.
	oneDayThreshold := minOfThresholds(threshold.OneDay, threshold.ThreeDay, threshold.SevenDay)
	threeDayThreshold := minOfThresholds(threshold.ThreeDay, threshold.SevenDay)

	validateBugFilingThresholdSatisfiesThresold(ctx, oneDayThreshold, bugFilingThres.OneDay, "one_day")
	validateBugFilingThresholdSatisfiesThresold(ctx, threeDayThreshold, bugFilingThres.ThreeDay, "three_day")
	validateBugFilingThresholdSatisfiesThresold(ctx, threshold.SevenDay, bugFilingThres.SevenDay, "seven_day")
}

func minOfThresholds(thresholds ...*int64) *int64 {
	var result *int64
	for _, t := range thresholds {
		if t != nil && (result == nil || *t < *result) {
			result = t
		}
	}
	return result
}

func validateBugFilingThresholdSatisfiesThresold(ctx *validation.Context, threshold *int64, bugFilingThres *int64, fieldName string) {
	ctx.Enter(fieldName)
	defer ctx.Exit()
	if bugFilingThres == nil {
		// Bugs are not filed based on this threshold.
		return
	}
	if *bugFilingThres < 0 {
		// The bug-filing threshold is invalid. This is already reported as an
		// error elsewhere.
		return
	}
	// If a bug may be filed at a particular threshold, it must also be
	// allowed to stay open at that threshold.
	if threshold == nil {
		ctx.Errorf("%s threshold must be set, with a value of at most %v (the configured bug-filing threshold). This ensures that bugs which are filed meet the criteria to stay open", fieldName, *bugFilingThres)
	} else if *threshold > *bugFilingThres {
		ctx.Errorf("value must be at most %v (the configured bug-filing threshold). This ensures that bugs which are filed meet the criteria to stay open", *bugFilingThres)
	}
}

func validateDisplayPrefix(ctx *validation.Context, prefix string) {
	ctx.Enter(prefix)
	defer ctx.Exit()
	if !prefixRE.MatchString(prefix) {
		ctx.Errorf("invalid display prefix: %q", prefix)
	}
}

func validateClustering(ctx *validation.Context, ca *configpb.Clustering) {
	ctx.Enter("clustering")
	defer ctx.Exit()

	if ca == nil {
		return
	}
	for i, r := range ca.TestNameRules {
		ctx.Enter("[%v]", i)
		validateTestNameRule(ctx, r)
		ctx.Exit()
	}
}

func validateTestNameRule(ctx *validation.Context, r *configpb.TestNameClusteringRule) {
	validateStringConfig(ctx, "name", r.Name, ruleNameRE)

	// Check the fields are non-empty. Their structure will be checked
	// by "Compile" below.
	validateStringConfig(ctx, "like_template", r.LikeTemplate, anyRE)
	validateStringConfig(ctx, "pattern", r.Pattern, anyRE)

	_, err := rules.Compile(r)
	if err != nil {
		ctx.Error(err)
	}
}
