// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

package s3

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	awsbase "github.com/hashicorp/aws-sdk-go-base/v2"
	baselogging "github.com/hashicorp/aws-sdk-go-base/v2/logging"
	"github.com/hashicorp/terraform/internal/backend"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/hashicorp/terraform/version"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
	"golang.org/x/exp/maps"
)

func New() backend.Backend {
	return &Backend{}
}

type LockType string

var (
	DBtype     = LockType("DB")
	BucketType = LockType("BUCKET")
)

type Backend struct {
	awsConfig aws.Config
	s3Client  *s3.Client
	dynClient *dynamodb.Client

	bucketName            string
	keyName               string
	serverSideEncryption  bool
	customerEncryptionKey []byte
	acl                   string
	kmsKeyID              string
	ddbTable              string
	workspaceKeyPrefix    string
	lockType              LockType
	lockName              string
}

// ConfigSchema returns a description of the expected configuration
// structure for the receiving backend.
func (b *Backend) ConfigSchema() *configschema.Block {
	return &configschema.Block{
		Attributes: map[string]*configschema.Attribute{
			"bucket": {
				Type:        cty.String,
				Required:    true,
				Description: "The name of the S3 bucket",
			},
			"lock_name": {
				Type:        cty.String,
				Optional:    true,
				Description: "the name of lock file into s3 Bucket",
			},
			"lock_type": {
				Type:        cty.String,
				Optional:    true,
				Description: "The name of the S3 bucket",
			},
			"key": {
				Type:        cty.String,
				Required:    true,
				Description: "The path to the state file inside the bucket",
			},
			"region": {
				Type:        cty.String,
				Optional:    true,
				Description: "AWS region of the S3 Bucket and DynamoDB Table (if used).",
			},
			"allowed_account_ids": {
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "List of allowed AWS account IDs.",
			},
			"dynamodb_endpoint": {
				Type:        cty.String,
				Optional:    true,
				Description: "A custom endpoint for the DynamoDB API",
				Deprecated:  true,
			},
			"ec2_metadata_service_endpoint": {
				Type:        cty.String,
				Optional:    true,
				Description: "Address of the EC2 metadata service (IMDS) endpoint to use.",
			},
			"ec2_metadata_service_endpoint_mode": {
				Type:        cty.String,
				Optional:    true,
				Description: "Mode to use in communicating with the metadata service.",
			},
			"endpoint": {
				Type:        cty.String,
				Optional:    true,
				Description: "A custom endpoint for the S3 API",
				Deprecated:  true,
			},
			"endpoints": {
				NestedType: &configschema.Object{
					Nesting: configschema.NestingSingle,
					Attributes: map[string]*configschema.Attribute{
						"dynamodb": {
							Type:        cty.String,
							Optional:    true,
							Description: "A custom endpoint for the DynamoDB API",
						},
						"iam": {
							Type:        cty.String,
							Optional:    true,
							Description: "A custom endpoint for the IAM API",
						},
						"s3": {
							Type:        cty.String,
							Optional:    true,
							Description: "A custom endpoint for the S3 API",
						},
						"sts": {
							Type:        cty.String,
							Optional:    true,
							Description: "A custom endpoint for the STS API",
						},
					},
				},
			},
			"forbidden_account_ids": {
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "List of forbidden AWS account IDs.",
			},
			"iam_endpoint": {
				Type:        cty.String,
				Optional:    true,
				Description: "A custom endpoint for the IAM API",
				Deprecated:  true,
			},
			"sts_endpoint": {
				Type:        cty.String,
				Optional:    true,
				Description: "A custom endpoint for the STS API",
				Deprecated:  true,
			},
			"sts_region": {
				Type:        cty.String,
				Optional:    true,
				Description: "AWS region for STS.",
			},
			"encrypt": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Whether to enable server side encryption of the state file",
			},
			"acl": {
				Type:        cty.String,
				Optional:    true,
				Description: "Canned ACL to be applied to the state file",
			},
			"access_key": {
				Type:        cty.String,
				Optional:    true,
				Description: "AWS access key",
			},
			"secret_key": {
				Type:        cty.String,
				Optional:    true,
				Description: "AWS secret key",
			},
			"kms_key_id": {
				Type:        cty.String,
				Optional:    true,
				Description: "The ARN of a KMS Key to use for encrypting the state",
			},
			"dynamodb_table": {
				Type:        cty.String,
				Optional:    true,
				Description: "DynamoDB table for state locking and consistency",
			},
			"profile": {
				Type:        cty.String,
				Optional:    true,
				Description: "AWS profile name",
			},
			"retry_mode": {
				Type:        cty.String,
				Optional:    true,
				Description: "Specifies how retries are attempted.",
			},
			"shared_config_files": {
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "List of paths to shared config files",
			},
			"shared_credentials_file": {
				Type:        cty.String,
				Optional:    true,
				Description: "Path to a shared credentials file",
				Deprecated:  true,
			},
			"shared_credentials_files": {
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "List of paths to shared credentials files",
			},
			"token": {
				Type:        cty.String,
				Optional:    true,
				Description: "MFA token",
			},
			"skip_credentials_validation": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Skip the credentials validation via STS API.",
			},
			"skip_metadata_api_check": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Skip the AWS Metadata API check.",
			},
			"skip_region_validation": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Skip static validation of region name.",
			},
			"sse_customer_key": {
				Type:        cty.String,
				Optional:    true,
				Description: "The base64-encoded encryption key to use for server-side encryption with customer-provided keys (SSE-C).",
				Sensitive:   true,
			},
			"role_arn": {
				Type:        cty.String,
				Optional:    true,
				Description: "The role to be assumed",
				Deprecated:  true,
			},
			"session_name": {
				Type:        cty.String,
				Optional:    true,
				Description: "The session name to use when assuming the role.",
				Deprecated:  true,
			},
			"external_id": {
				Type:        cty.String,
				Optional:    true,
				Description: "The external ID to use when assuming the role",
				Deprecated:  true,
			},

			"assume_role_duration_seconds": {
				Type:        cty.Number,
				Optional:    true,
				Description: "Seconds to restrict the assume role session duration.",
				Deprecated:  true,
			},

			"assume_role_policy": {
				Type:        cty.String,
				Optional:    true,
				Description: "IAM Policy JSON describing further restricting permissions for the IAM Role being assumed.",
				Deprecated:  true,
			},

			"assume_role_policy_arns": {
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "Amazon Resource Names (ARNs) of IAM Policies describing further restricting permissions for the IAM Role being assumed.",
				Deprecated:  true,
			},

			"assume_role_tags": {
				Type:        cty.Map(cty.String),
				Optional:    true,
				Description: "Assume role session tags.",
				Deprecated:  true,
			},

			"assume_role_transitive_tag_keys": {
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "Assume role session tag keys to pass to any subsequent sessions.",
				Deprecated:  true,
			},

			"workspace_key_prefix": {
				Type:        cty.String,
				Optional:    true,
				Description: "The prefix applied to the non-default state path inside the bucket.",
			},

			"force_path_style": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Enable path-style S3 URLs.",
				Deprecated:  true,
			},

			"use_path_style": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Enable path-style S3 URLs.",
			},

			"max_retries": {
				Type:        cty.Number,
				Optional:    true,
				Description: "The maximum number of times an AWS API request is retried on retryable failure.",
			},

			"assume_role": {
				NestedType: &configschema.Object{
					Nesting:    configschema.NestingSingle,
					Attributes: assumeRoleFullSchema().SchemaAttributes(),
				},
			},

			"assume_role_with_web_identity": {
				NestedType: &configschema.Object{
					Nesting:    configschema.NestingSingle,
					Attributes: assumeRoleWithWebIdentityFullSchema().SchemaAttributes(),
				},
			},

			"use_legacy_workflow": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Use the legacy authentication workflow, preferring environment variables over backend configuration.",
			},

			"custom_ca_bundle": {
				Type:        cty.String,
				Optional:    true,
				Description: "File containing custom root and intermediate certificates.",
			},

			"http_proxy": {
				Type:        cty.String,
				Optional:    true,
				Description: "Address of an HTTP proxy to use when accessing the AWS API.",
			},
			"insecure": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Whether to explicitly allow the backend to perform insecure SSL requests.",
			},
			"use_fips_endpoint": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Force the backend to resolve endpoints with FIPS capability.",
			},
			"use_dualstack_endpoint": {
				Type:        cty.Bool,
				Optional:    true,
				Description: "Force the backend to resolve endpoints with DualStack capability.",
			},
		},
	}
}

// PrepareConfig checks the validity of the values in the given
// configuration, and inserts any missing defaults, assuming that its
// structure has already been validated per the schema returned by
// ConfigSchema.
func (b *Backend) PrepareConfig(obj cty.Value) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	if obj.IsNull() {
		return obj, diags
	}

	var attrPath cty.Path

	attrPath = cty.GetAttrPath("bucket")
	if val := obj.GetAttr("bucket"); val.IsNull() {
		diags = diags.Append(requiredAttributeErrDiag(attrPath))
	} else {
		bucketValidators := validateString{
			Validators: []stringValidator{
				validateStringNotEmpty,
			},
		}
		bucketValidators.ValidateAttr(val, attrPath, &diags)
	}

	attrPath = cty.GetAttrPath("key")
	if val := obj.GetAttr("key"); val.IsNull() {
		diags = diags.Append(requiredAttributeErrDiag(attrPath))
	} else {
		keyValidators := validateString{
			Validators: []stringValidator{
				validateStringNotEmpty,
				validateStringS3Path,
				validateStringDoesNotContain("//"),
			},
		}
		keyValidators.ValidateAttr(val, attrPath, &diags)
	}

	// Not updating region handling, because validation will be handled by `aws-sdk-go-base` once it is updated
	if val := obj.GetAttr("region"); val.IsNull() || val.AsString() == "" {
		if os.Getenv("AWS_REGION") == "" && os.Getenv("AWS_DEFAULT_REGION") == "" {
			diags = diags.Append(tfdiags.AttributeValue(
				tfdiags.Error,
				"Missing region value",
				`The "region" attribute or the "AWS_REGION" or "AWS_DEFAULT_REGION" environment variables must be set.`,
				cty.GetAttrPath("region"),
			))
		}
	}

	validateAttributesConflict(
		cty.GetAttrPath("kms_key_id"),
		cty.GetAttrPath("sse_customer_key"),
	)(obj, cty.Path{}, &diags)

	attrPath = cty.GetAttrPath("kms_key_id")
	if val := obj.GetAttr("kms_key_id"); !val.IsNull() {
		kmsKeyIDValidators := validateString{
			Validators: []stringValidator{
				validateStringKMSKey,
			},
		}
		kmsKeyIDValidators.ValidateAttr(val, attrPath, &diags)
	}

	attrPath = cty.GetAttrPath("workspace_key_prefix")
	if val := obj.GetAttr("workspace_key_prefix"); !val.IsNull() {
		keyPrefixValidators := validateString{
			Validators: []stringValidator{
				validateStringS3Path,
			},
		}
		keyPrefixValidators.ValidateAttr(val, attrPath, &diags)
	}

	var assumeRoleDeprecatedFields = map[string]string{
		"role_arn":                        "assume_role.role_arn",
		"session_name":                    "assume_role.session_name",
		"external_id":                     "assume_role.external_id",
		"assume_role_duration_seconds":    "assume_role.duration",
		"assume_role_policy":              "assume_role.policy",
		"assume_role_policy_arns":         "assume_role.policy_arns",
		"assume_role_tags":                "assume_role.tags",
		"assume_role_transitive_tag_keys": "assume_role.transitive_tag_keys",
	}

	if val := obj.GetAttr("assume_role"); !val.IsNull() {
		diags = diags.Append(prepareAssumeRoleConfig(val, cty.GetAttrPath("assume_role")))

		if defined := findDeprecatedFields(obj, assumeRoleDeprecatedFields); len(defined) != 0 {
			diags = diags.Append(tfdiags.WholeContainingBody(
				tfdiags.Error,
				"Conflicting Parameters",
				`The following deprecated parameters conflict with the parameter "assume_role". Replace them as follows:`+"\n"+
					formatDeprecations(defined),
			))
		}
	} else {
		if defined := findDeprecatedFields(obj, assumeRoleDeprecatedFields); len(defined) != 0 {
			diags = diags.Append(wholeBodyWarningDiag(
				"Deprecated Parameters",
				`The following parameters have been deprecated. Replace them as follows:`+"\n"+
					formatDeprecations(defined),
			))
		}
	}

	if val := obj.GetAttr("assume_role_with_web_identity"); !val.IsNull() {
		diags = diags.Append(prepareAssumeRoleWithWebIdentityConfig(val, cty.GetAttrPath("assume_role_with_web_identity")))
	}

	validateAttributesConflict(
		cty.GetAttrPath("shared_credentials_file"),
		cty.GetAttrPath("shared_credentials_files"),
	)(obj, cty.Path{}, &diags)

	attrPath = cty.GetAttrPath("shared_credentials_file")
	if val := obj.GetAttr("shared_credentials_file"); !val.IsNull() {
		diags = diags.Append(deprecatedAttrDiag(attrPath, cty.GetAttrPath("shared_credentials_files")))
	}

	endpointFields := map[string]string{
		"dynamodb_endpoint": "dynamodb",
		"iam_endpoint":      "iam",
		"endpoint":          "s3",
		"sts_endpoint":      "sts",
	}
	endpoints := make(map[string]string)
	if val := obj.GetAttr("endpoints"); !val.IsNull() {
		for _, k := range []string{"dynamodb", "iam", "s3", "sts"} {
			if v := val.GetAttr(k); !v.IsNull() {
				endpoints[k] = v.AsString()
			}
		}
	}
	for k, v := range endpointFields {
		if val := obj.GetAttr(k); !val.IsNull() {
			diags = diags.Append(deprecatedAttrDiag(cty.GetAttrPath(k), cty.GetAttrPath("endpoints").GetAttr(v)))
			if _, ok := endpoints[v]; ok {
				diags = diags.Append(wholeBodyErrDiag(
					"Conflicting Parameters",
					fmt.Sprintf(`The parameters "%s" and "%s" cannot be configured together.`,
						pathString(cty.GetAttrPath(k)),
						pathString(cty.GetAttrPath("endpoints").GetAttr(v)),
					),
				))
			}
		}
	}

	endpointValidators := validateString{
		Validators: []stringValidator{
			validateStringURL,
		},
	}
	if val := obj.GetAttr("endpoints"); !val.IsNull() {
		attrPath := cty.GetAttrPath("endpoints")
		for _, k := range []string{"dynamodb", "iam", "s3", "sts"} {
			if v := val.GetAttr(k); !v.IsNull() {
				attrPath := attrPath.GetAttr(k)
				endpointValidators.ValidateAttr(v, attrPath, &diags)
			}
		}
	}
	for _, k := range maps.Keys(endpointFields) {
		if val := obj.GetAttr(k); !val.IsNull() {
			attrPath := cty.GetAttrPath(k)
			endpointValidators.ValidateAttr(val, attrPath, &diags)
		}
	}
	if val := obj.GetAttr("ec2_metadata_service_endpoint"); !val.IsNull() {
		attrPath := cty.GetAttrPath("ec2_metadata_service_endpoint")
		endpointValidators.ValidateAttr(val, attrPath, &diags)
	}

	validateAttributesConflict(
		cty.GetAttrPath("force_path_style"),
		cty.GetAttrPath("use_path_style"),
	)(obj, cty.Path{}, &diags)

	attrPath = cty.GetAttrPath("force_path_style")
	if val := obj.GetAttr("force_path_style"); !val.IsNull() {
		diags = diags.Append(deprecatedAttrDiag(attrPath, cty.GetAttrPath("use_path_style")))
	}

	attrPath = cty.GetAttrPath("retry_mode")
	if val := obj.GetAttr("retry_mode"); !val.IsNull() {
		retryModeValidators := validateString{
			Validators: []stringValidator{
				validateStringRetryMode,
			},
		}
		retryModeValidators.ValidateAttr(val, attrPath, &diags)
	}

	attrPath = cty.GetAttrPath("ec2_metadata_service_endpoint_mode")
	if val := obj.GetAttr("ec2_metadata_service_endpoint_mode"); !val.IsNull() {
		endpointModeValidators := validateString{
			Validators: []stringValidator{
				validateStringInSlice(awsbase.EC2MetadataEndpointMode_Values()),
			},
		}
		endpointModeValidators.ValidateAttr(val, attrPath, &diags)
	}

	validateAttributesConflict(
		cty.GetAttrPath("allowed_account_ids"),
		cty.GetAttrPath("forbidden_account_ids"),
	)(obj, cty.Path{}, &diags)

	return obj, diags
}

func findDeprecatedFields(obj cty.Value, attrs map[string]string) map[string]string {
	defined := make(map[string]string)
	for attr, v := range attrs {
		if val := obj.GetAttr(attr); !val.IsNull() {
			defined[attr] = v
		}
	}
	return defined
}

func formatDeprecations(attrs map[string]string) string {
	names := make([]string, 0, len(attrs))
	var maxLen int
	for attr := range attrs {
		names = append(names, attr)
		if l := len(attr); l > maxLen {
			maxLen = l
		}
	}
	sort.Strings(names)

	var buf strings.Builder

	for _, attr := range names {
		replacement := attrs[attr]
		fmt.Fprintf(&buf, "  * %-[1]*[2]s -> %[3]s\n", maxLen, attr, replacement)
	}
	return buf.String()
}

// Configure uses the provided configuration to set configuration fields
// within the backend.
//
// The given configuration is assumed to have already been validated
// against the schema returned by ConfigSchema and passed validation
// via PrepareConfig.
func (b *Backend) Configure(obj cty.Value) tfdiags.Diagnostics {
	ctx := context.TODO()
	log := logger()
	log = logWithOperation(log, operationBackendConfigure)

	var diags tfdiags.Diagnostics
	if obj.IsNull() {
		return diags
	}

	var region string
	if v, ok := stringAttrOk(obj, "region"); ok {
		region = v
	}

	if region != "" && !boolAttr(obj, "skip_region_validation") {
		if err := awsbase.ValidateRegion(region); err != nil {
			diags = diags.Append(tfdiags.AttributeValue(
				tfdiags.Error,
				"Invalid region value",
				err.Error(),
				cty.GetAttrPath("region"),
			))
			return diags
		}
	}
	b.lockType = LockType(stringAttr(obj, "lock_type"))
	b.lockName = stringAttr(obj, "lock_name")
	b.bucketName = stringAttr(obj, "bucket")
	b.keyName = stringAttr(obj, "key")

	log = log.With(
		logKeyBucket, b.bucketName,
		logKeyPath, b.keyName,
	)

	b.acl = stringAttr(obj, "acl")
	b.workspaceKeyPrefix = stringAttrDefault(obj, "workspace_key_prefix", "env:")
	b.serverSideEncryption = boolAttr(obj, "encrypt")
	b.kmsKeyID = stringAttr(obj, "kms_key_id")
	b.ddbTable = stringAttr(obj, "dynamodb_table")

	if _, ok := stringAttrOk(obj, "kms_key_id"); ok {
		if customerKey := os.Getenv("AWS_SSE_CUSTOMER_KEY"); customerKey != "" {
			diags = diags.Append(wholeBodyErrDiag(
				"Invalid encryption configuration",
				encryptionKeyConflictEnvVarError,
			))
		}
	}

	if customerKey, ok := stringAttrOk(obj, "sse_customer_key"); ok {
		if len(customerKey) != 44 {
			diags = diags.Append(tfdiags.AttributeValue(
				tfdiags.Error,
				"Invalid sse_customer_key value",
				"sse_customer_key must be 44 characters in length",
				cty.GetAttrPath("sse_customer_key"),
			))
		} else {
			var err error
			if b.customerEncryptionKey, err = base64.StdEncoding.DecodeString(customerKey); err != nil {
				diags = diags.Append(tfdiags.AttributeValue(
					tfdiags.Error,
					"Invalid sse_customer_key value",
					fmt.Sprintf("sse_customer_key must be base64 encoded: %s", err),
					cty.GetAttrPath("sse_customer_key"),
				))
			}
		}
	} else if customerKey := os.Getenv("AWS_SSE_CUSTOMER_KEY"); customerKey != "" {
		if len(customerKey) != 44 {
			diags = diags.Append(tfdiags.WholeContainingBody(
				tfdiags.Error,
				"Invalid AWS_SSE_CUSTOMER_KEY value",
				`The environment variable "AWS_SSE_CUSTOMER_KEY" must be 44 characters in length`,
			))
		} else {
			var err error
			if b.customerEncryptionKey, err = base64.StdEncoding.DecodeString(customerKey); err != nil {
				diags = diags.Append(tfdiags.WholeContainingBody(
					tfdiags.Error,
					"Invalid AWS_SSE_CUSTOMER_KEY value",
					fmt.Sprintf(`The environment variable "AWS_SSE_CUSTOMER_KEY" must be base64 encoded: %s`, err),
				))
			}
		}
	}

	endpointEnvvars := map[string]string{
		"AWS_DYNAMODB_ENDPOINT": "AWS_ENDPOINT_URL_DYNAMODB",
		"AWS_IAM_ENDPOINT":      "AWS_ENDPOINT_URL_IAM",
		"AWS_S3_ENDPOINT":       "AWS_ENDPOINT_URL_S3",
		"AWS_STS_ENDPOINT":      "AWS_ENDPOINT_URL_STS",
		"AWS_METADATA_URL":      "AWS_EC2_METADATA_SERVICE_ENDPOINT",
	}
	for envvar, replacement := range endpointEnvvars {
		if val := os.Getenv(envvar); val != "" {
			diags = diags.Append(deprecatedEnvVarDiag(envvar, replacement))
		}
	}

	ctx, baselog := baselogging.NewHcLogger(ctx, log)

	cfg := &awsbase.Config{
		AccessKey:              stringAttr(obj, "access_key"),
		APNInfo:                stdUserAgentProducts(),
		CallerDocumentationURL: "https://www.terraform.io/docs/language/settings/backends/s3.html",
		CallerName:             "S3 Backend",
		Logger:                 baselog,
		MaxRetries:             intAttrDefault(obj, "max_retries", 5),
		Profile:                stringAttr(obj, "profile"),
		Region:                 stringAttr(obj, "region"),
		SecretKey:              stringAttr(obj, "secret_key"),
		SkipCredsValidation:    boolAttr(obj, "skip_credentials_validation"),
		Token:                  stringAttr(obj, "token"),
	}

	// The "legacy" authentication workflow used in aws-sdk-go-base V1 will be
	// gradually phased out over several Terraform minor versions:
	//
	// 1.6 - Default to `true` (prefer existing behavior, "opt-out" for new behavior)
	// 1.7 - Default to `false` (prefer new behavior, "opt-in" for legacy behavior)
	// 1.8 - Remove argument, legacy workflow no longer supported
	if val, ok := boolAttrOk(obj, "use_legacy_workflow"); ok {
		cfg.UseLegacyWorkflow = val
	} else {
		cfg.UseLegacyWorkflow = true
	}

	if val, ok := boolAttrOk(obj, "skip_metadata_api_check"); ok {
		if val {
			cfg.EC2MetadataServiceEnableState = imds.ClientDisabled
		} else {
			cfg.EC2MetadataServiceEnableState = imds.ClientEnabled
		}
	}

	if v, ok := retrieveArgument(&diags,
		newAttributeRetriever(obj, cty.GetAttrPath("ec2_metadata_service_endpoint")),
		newEnvvarRetriever("AWS_EC2_METADATA_SERVICE_ENDPOINT"),
		newEnvvarRetriever("AWS_METADATA_URL"),
	); ok {
		cfg.EC2MetadataServiceEndpoint = v
	}

	if v, ok := retrieveArgument(&diags,
		newAttributeRetriever(obj, cty.GetAttrPath("ec2_metadata_service_endpoint_mode")),
		newEnvvarRetriever("AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE"),
	); ok {
		cfg.EC2MetadataServiceEndpointMode = v
	}

	if val, ok := stringAttrOk(obj, "shared_credentials_file"); ok {
		cfg.SharedCredentialsFiles = []string{
			val,
		}
	}
	if val, ok := stringSetAttrDefaultEnvVarOk(obj, "shared_credentials_files", "AWS_SHARED_CREDENTIALS_FILE"); ok {
		cfg.SharedCredentialsFiles = val
	}
	if val, ok := stringSetAttrDefaultEnvVarOk(obj, "shared_config_files", "AWS_SHARED_CONFIG_FILE"); ok {
		cfg.SharedConfigFiles = val
	}

	if v, ok := retrieveArgument(&diags,
		newAttributeRetriever(obj, cty.GetAttrPath("custom_ca_bundle")),
		newEnvvarRetriever("AWS_CA_BUNDLE"),
	); ok {
		cfg.CustomCABundle = v
	}

	if v, ok := retrieveArgument(&diags,
		newAttributeRetriever(obj, cty.GetAttrPath("endpoints").GetAttr("iam")),
		newAttributeRetriever(obj, cty.GetAttrPath("iam_endpoint")),
		newEnvvarRetriever("AWS_ENDPOINT_URL_IAM"),
		newEnvvarRetriever("AWS_IAM_ENDPOINT"),
	); ok {
		cfg.IamEndpoint = v
	}

	if v, ok := retrieveArgument(&diags,
		newAttributeRetriever(obj, cty.GetAttrPath("endpoints").GetAttr("sts")),
		newAttributeRetriever(obj, cty.GetAttrPath("sts_endpoint")),
		newEnvvarRetriever("AWS_ENDPOINT_URL_STS"),
		newEnvvarRetriever("AWS_STS_ENDPOINT"),
	); ok {
		cfg.StsEndpoint = v
	}

	if v, ok := retrieveArgument(&diags, newAttributeRetriever(obj, cty.GetAttrPath("sts_region"))); ok {
		cfg.StsRegion = v
	}

	if assumeRole := obj.GetAttr("assume_role"); !assumeRole.IsNull() {
		ar := &awsbase.AssumeRole{}
		if val, ok := stringAttrOk(assumeRole, "role_arn"); ok {
			ar.RoleARN = val
		}
		if val, ok := stringAttrOk(assumeRole, "duration"); ok {
			duration, _ := time.ParseDuration(val)
			ar.Duration = duration
		}
		if val, ok := stringAttrOk(assumeRole, "external_id"); ok {
			ar.ExternalID = val
		}
		if val, ok := stringAttrOk(assumeRole, "policy"); ok {
			ar.Policy = strings.TrimSpace(val)
		}
		if val, ok := stringSetAttrOk(assumeRole, "policy_arns"); ok {
			ar.PolicyARNs = val
		}
		if val, ok := stringAttrOk(assumeRole, "session_name"); ok {
			ar.SessionName = val
		}
		if val, ok := stringMapAttrOk(assumeRole, "tags"); ok {
			ar.Tags = val
		}
		if val, ok := stringSetAttrOk(assumeRole, "transitive_tag_keys"); ok {
			ar.TransitiveTagKeys = val
		}
		cfg.AssumeRole = ar
	} else if arn, ok := stringAttrOk(obj, "role_arn"); ok {
		ar := &awsbase.AssumeRole{}
		ar.RoleARN = arn
		ar.SessionName = stringAttr(obj, "session_name")
		ar.Duration = time.Duration(intAttr(obj, "assume_role_duration_seconds")) * time.Second
		ar.ExternalID = stringAttr(obj, "external_id")
		if val, ok := stringAttrOk(obj, "assume_role_policy"); ok {
			ar.Policy = strings.TrimSpace(val)
		}
		if val, ok := stringSetAttrOk(obj, "assume_role_policy_arns"); ok {
			ar.PolicyARNs = val
		}

		if val, ok := stringMapAttrOk(obj, "assume_role_tags"); ok {
			ar.Tags = val
		}

		if val, ok := stringSetAttrOk(obj, "assume_role_transitive_tag_keys"); ok {
			ar.TransitiveTagKeys = val
		}
		cfg.AssumeRole = ar
	}

	if assumeRoleWithWebIdentity := obj.GetAttr("assume_role_with_web_identity"); !assumeRoleWithWebIdentity.IsNull() {
		ar := &awsbase.AssumeRoleWithWebIdentity{}
		if val, ok := stringAttrOk(assumeRoleWithWebIdentity, "role_arn"); ok {
			ar.RoleARN = val
		}
		if val, ok := stringAttrOk(assumeRoleWithWebIdentity, "duration"); ok {
			duration, _ := time.ParseDuration(val)
			ar.Duration = duration
		}
		if val, ok := stringAttrOk(assumeRoleWithWebIdentity, "policy"); ok {
			ar.Policy = strings.TrimSpace(val)
		}
		if val, ok := stringSetAttrOk(assumeRoleWithWebIdentity, "policy_arns"); ok {
			ar.PolicyARNs = val
		}
		if val, ok := stringAttrOk(assumeRoleWithWebIdentity, "session_name"); ok {
			ar.SessionName = val
		}
		if val, ok := stringAttrOk(assumeRoleWithWebIdentity, "web_identity_token"); ok {
			ar.WebIdentityToken = val
		}
		if val, ok := stringAttrOk(assumeRoleWithWebIdentity, "web_identity_token_file"); ok {
			ar.WebIdentityTokenFile = val
		}
		cfg.AssumeRoleWithWebIdentity = ar
	}

	if v, ok := retrieveArgument(&diags,
		newAttributeRetriever(obj, cty.GetAttrPath("http_proxy")),
		newEnvvarRetriever("HTTP_PROXY"),
		newEnvvarRetriever("HTTPS_PROXY"),
	); ok {
		cfg.HTTPProxy = v
	}
	if val, ok := boolAttrOk(obj, "insecure"); ok {
		cfg.Insecure = val
	}
	if val, ok := boolAttrDefaultEnvVarOk(obj, "use_fips_endpoint", "AWS_USE_FIPS_ENDPOINT"); ok {
		cfg.UseFIPSEndpoint = val
	}
	if val, ok := boolAttrDefaultEnvVarOk(obj, "use_dualstack_endpoint", "AWS_USE_DUALSTACK_ENDPOINT"); ok {
		cfg.UseDualStackEndpoint = val
	}

	if v, ok := retrieveArgument(&diags,
		newAttributeRetriever(obj, cty.GetAttrPath("retry_mode")),
		newEnvvarRetriever("AWS_RETRY_MODE"),
	); ok {
		cfg.RetryMode = aws.RetryMode(v)
	}

	if val, ok := stringSetAttrOk(obj, "allowed_account_ids"); ok {
		cfg.AllowedAccountIds = val
	}
	if val, ok := stringSetAttrOk(obj, "forbidden_account_ids"); ok {
		cfg.ForbiddenAccountIds = val
	}

	_ /* ctx */, awsConfig, cfgDiags := awsbase.GetAwsConfig(ctx, cfg)
	for _, d := range cfgDiags {
		diags = diags.Append(tfdiags.Sourceless(
			baseSeverityToTerraformSeverity(d.Severity()),
			d.Summary(),
			d.Detail(),
		))
	}
	if diags.HasErrors() {
		return diags
	}
	b.awsConfig = awsConfig

	accountID, _, awsDiags := awsbase.GetAwsAccountIDAndPartition(ctx, awsConfig, cfg)
	for _, d := range awsDiags {
		diags = append(diags, tfdiags.Sourceless(
			baseSeverityToTerraformSeverity(d.Severity()),
			fmt.Sprintf("Retrieving AWS account details: %s", d.Summary()),
			d.Detail(),
		))
	}

	err := cfg.VerifyAccountIDAllowed(accountID)
	if err != nil {
		diags = append(diags, tfdiags.Sourceless(
			tfdiags.Error,
			"Invalid account ID",
			err.Error(),
		))
	}

	b.dynClient = dynamodb.NewFromConfig(awsConfig, func(opts *dynamodb.Options) {
		if v, ok := retrieveArgument(&diags,
			newAttributeRetriever(obj, cty.GetAttrPath("endpoints").GetAttr("dynamodb")),
			newAttributeRetriever(obj, cty.GetAttrPath("dynamodb_endpoint")),
			newEnvvarRetriever("AWS_ENDPOINT_URL_DYNAMODB"),
			newEnvvarRetriever("AWS_DYNAMODB_ENDPOINT"),
		); ok {
			opts.EndpointResolver = dynamodb.EndpointResolverFromURL(v) //nolint:staticcheck // The replacement is not documented yet (2023/08/03)
		}
	})

	b.s3Client = s3.NewFromConfig(awsConfig, func(opts *s3.Options) {
		if v, ok := retrieveArgument(&diags,
			newAttributeRetriever(obj, cty.GetAttrPath("endpoints").GetAttr("s3")),
			newAttributeRetriever(obj, cty.GetAttrPath("endpoint")),
			newEnvvarRetriever("AWS_ENDPOINT_URL_S3"),
			newEnvvarRetriever("AWS_S3_ENDPOINT"),
		); ok {
			opts.EndpointResolver = s3.EndpointResolverFromURL(v) //nolint:staticcheck // The replacement is not documented yet (2023/08/03)
		}
		if v, ok := boolAttrOk(obj, "force_path_style"); ok { // deprecated
			opts.UsePathStyle = v
		}
		if v, ok := boolAttrOk(obj, "use_path_style"); ok {
			opts.UsePathStyle = v
		}
	})

	return diags
}

func stdUserAgentProducts() *awsbase.APNInfo {
	return &awsbase.APNInfo{
		PartnerName: "HashiCorp",
		Products: []awsbase.UserAgentProduct{
			{Name: "Terraform", Version: version.String(), Comment: "+https://www.terraform.io"},
		},
	}
}

type argumentRetriever interface {
	Retrieve(diags *tfdiags.Diagnostics) (string, bool)
}

type attributeRetriever struct {
	obj      cty.Value
	objPath  cty.Path
	attrPath cty.Path
}

var _ argumentRetriever = attributeRetriever{}

func newAttributeRetriever(obj cty.Value, attrPath cty.Path) attributeRetriever {
	return attributeRetriever{
		obj:      obj,
		objPath:  cty.Path{}, // Assumes that we're working relative to the root object
		attrPath: attrPath,
	}
}

func (r attributeRetriever) Retrieve(diags *tfdiags.Diagnostics) (string, bool) {
	val, err := pathSafeApply(r.attrPath, r.obj)
	if err != nil {
		*diags = diags.Append(attributeErrDiag(
			"Invalid Path for Schema",
			"The S3 Backend unexpectedly provided a path that does not match the schema. "+
				"Please report this to the developers.\n\n"+
				"Path: "+pathString(r.attrPath)+"\n\n"+
				"Error: "+err.Error(),
			r.objPath,
		))
	}
	return stringValueOk(val)
}

// pathSafeApply applies a `cty.Path` to a `cty.Value`.
// Unlike `path.Apply`, it does not return an error if it encounters a Null value
func pathSafeApply(path cty.Path, obj cty.Value) (cty.Value, error) {
	if obj == cty.NilVal || obj.IsNull() {
		return obj, nil
	}
	val := obj
	var err error
	for _, step := range path {
		val, err = step.Apply(val)
		if err != nil {
			return cty.NilVal, err
		}
		if val == cty.NilVal || val.IsNull() {
			return val, nil
		}
	}
	return val, nil
}

type envvarRetriever struct {
	name string
}

var _ argumentRetriever = envvarRetriever{}

func newEnvvarRetriever(name string) envvarRetriever {
	return envvarRetriever{
		name: name,
	}
}

func (r envvarRetriever) Retrieve(_ *tfdiags.Diagnostics) (string, bool) {
	if v := os.Getenv(r.name); v != "" {
		return v, true
	}
	return "", false
}

func retrieveArgument(diags *tfdiags.Diagnostics, retrievers ...argumentRetriever) (string, bool) {
	for _, retriever := range retrievers {
		if v, ok := retriever.Retrieve(diags); ok {
			return v, true
		}
	}
	return "", false
}

func stringValue(val cty.Value) string {
	v, _ := stringValueOk(val)
	return v
}

func stringValueOk(val cty.Value) (string, bool) {
	if val.IsNull() {
		return "", false
	} else {
		return val.AsString(), true
	}
}

func stringAttr(obj cty.Value, name string) string {
	return stringValue(obj.GetAttr(name))
}

func stringAttrOk(obj cty.Value, name string) (string, bool) {
	return stringValueOk(obj.GetAttr(name))
}

func stringAttrDefault(obj cty.Value, name, def string) string {
	if v, ok := stringAttrOk(obj, name); !ok {
		return def
	} else {
		return v
	}
}

func stringSetValueOk(val cty.Value) ([]string, bool) {
	var list []string
	typ := val.Type()
	if !typ.IsSetType() {
		return nil, false
	}
	err := gocty.FromCtyValue(val, &list)
	if err != nil {
		return nil, false
	}
	return list, true
}

func stringSetAttrOk(obj cty.Value, name string) ([]string, bool) {
	return stringSetValueOk(obj.GetAttr(name))
}

// stringSetAttrDefaultEnvVarOk checks for a configured set of strings
// in the provided argument name or environment variables. A configured
// argument takes precedent over environment variables. An environment
// variable is assumed to be as a single item, such as how the singular
// AWS_SHARED_CONFIG_FILE variable aligns with the underlying
// shared_config_files argument.
func stringSetAttrDefaultEnvVarOk(obj cty.Value, name string, envvars ...string) ([]string, bool) {
	if v, ok := stringSetValueOk(obj.GetAttr(name)); !ok {
		for _, envvar := range envvars {
			if v := os.Getenv(envvar); v != "" {
				return []string{v}, true
			}
		}
		return nil, false
	} else {
		return v, true
	}
}

func stringMapValueOk(val cty.Value) (map[string]string, bool) {
	var m map[string]string
	err := gocty.FromCtyValue(val, &m)
	if err != nil {
		return nil, false
	}
	return m, true
}

func stringMapAttrOk(obj cty.Value, name string) (map[string]string, bool) {
	return stringMapValueOk(obj.GetAttr(name))
}

func boolAttr(obj cty.Value, name string) bool {
	v, _ := boolAttrOk(obj, name)
	return v
}

func boolAttrOk(obj cty.Value, name string) (bool, bool) {
	if val := obj.GetAttr(name); val.IsNull() {
		return false, false
	} else {
		return val.True(), true
	}
}

// boolAttrDefaultEnvVarOk checks for a configured bool argument or a non-empty
// value in any of the provided environment variables. If any of the environment
// variables are non-empty, to boolean is considered true.
func boolAttrDefaultEnvVarOk(obj cty.Value, name string, envvars ...string) (bool, bool) {
	if val := obj.GetAttr(name); val.IsNull() {
		for _, envvar := range envvars {
			if v := os.Getenv(envvar); v != "" {
				return true, true
			}
		}
		return false, false
	} else {
		return val.True(), true
	}
}

func intAttr(obj cty.Value, name string) int {
	v, _ := intAttrOk(obj, name)
	return v
}

func intAttrOk(obj cty.Value, name string) (int, bool) {
	if val := obj.GetAttr(name); val.IsNull() {
		return 0, false
	} else {
		var v int
		if err := gocty.FromCtyValue(val, &v); err != nil {
			return 0, false
		}
		return v, true
	}
}

func intAttrDefault(obj cty.Value, name string, def int) int {
	if v, ok := intAttrOk(obj, name); !ok {
		return def
	} else {
		return v
	}
}

const encryptionKeyConflictEnvVarError = `Only one of "kms_key_id" and the environment variable "AWS_SSE_CUSTOMER_KEY" can be set.

The "kms_key_id" is used for encryption with KMS-Managed Keys (SSE-KMS)
while "AWS_SSE_CUSTOMER_KEY" is used for encryption with customer-managed keys (SSE-C).
Please choose one or the other.`

func prepareAssumeRoleConfig(obj cty.Value, objPath cty.Path) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if obj.IsNull() {
		return diags
	}

	for name, attrSchema := range assumeRoleFullSchema() {
		attrPath := objPath.GetAttr(name)
		attrVal := obj.GetAttr(name)

		if a, e := attrVal.Type(), attrSchema.SchemaAttribute().Type; a != e {
			diags = diags.Append(attributeErrDiag(
				"Internal Error",
				fmt.Sprintf(`Expected type to be %s, got: %s`, e.FriendlyName(), a.FriendlyName()),
				attrPath,
			))
			continue
		}

		if attrVal.IsNull() {
			if attrSchema.SchemaAttribute().Required {
				diags = diags.Append(requiredAttributeErrDiag(attrPath))
			}
			continue
		}

		validator := attrSchema.Validator()
		validator.ValidateAttr(attrVal, attrPath, &diags)
	}

	return diags
}
func prepareAssumeRoleWithWebIdentityConfig(obj cty.Value, objPath cty.Path) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics
	if obj.IsNull() {
		return diags
	}

	for name, attrSchema := range assumeRoleWithWebIdentityFullSchema() {
		attrPath := objPath.GetAttr(name)
		attrVal := obj.GetAttr(name)

		if a, e := attrVal.Type(), attrSchema.SchemaAttribute().Type; a != e {
			diags = diags.Append(attributeErrDiag(
				"Internal Error",
				fmt.Sprintf(`Expected type to be %s, got: %s`, e.FriendlyName(), a.FriendlyName()),
				attrPath,
			))
			continue
		}

		if attrVal.IsNull() {
			if attrSchema.SchemaAttribute().Required {
				diags = diags.Append(requiredAttributeErrDiag(attrPath))
			}
			continue
		}

		validator := attrSchema.Validator()
		validator.ValidateAttr(attrVal, attrPath, &diags)
	}

	validateExactlyOneOfAttributes(
		cty.GetAttrPath("web_identity_token"),
		cty.GetAttrPath("web_identity_token_file"),
	)(obj, objPath, &diags)

	return diags
}

func requiredAttributeErrDiag(path cty.Path) tfdiags.Diagnostic {
	return attributeErrDiag(
		"Missing Required Value",
		fmt.Sprintf("The attribute %q is required by the backend.\n\n", pathString(path))+
			"Refer to the backend documentation for additional information which attributes are required.",
		path,
	)
}

func pathString(path cty.Path) string {
	var buf strings.Builder
	for i, step := range path {
		switch x := step.(type) {
		case cty.GetAttrStep:
			if i != 0 {
				buf.WriteString(".")
			}
			buf.WriteString(x.Name)
		case cty.IndexStep:
			val := x.Key
			typ := val.Type()
			var s string
			switch {
			case typ == cty.String:
				s = val.AsString()
			case typ == cty.Number:
				num := val.AsBigFloat()
				s = num.String()
			default:
				s = fmt.Sprintf("<unexpected index: %s>", typ.FriendlyName())
			}
			buf.WriteString(fmt.Sprintf("[%s]", s))
		default:
			if i != 0 {
				buf.WriteString(".")
			}
			buf.WriteString(fmt.Sprintf("<unexpected step: %[1]T %[1]v>", x))
		}
	}
	return buf.String()
}

type validateSchema interface {
	ValidateAttr(cty.Value, cty.Path, *tfdiags.Diagnostics)
}

type validateString struct {
	Validators []stringValidator
}

func (v validateString) ValidateAttr(val cty.Value, attrPath cty.Path, diags *tfdiags.Diagnostics) {
	s := val.AsString()
	for _, validator := range v.Validators {
		validator(s, attrPath, diags)
		if diags.HasErrors() {
			return
		}
	}
}

type validateMap struct{}

func (v validateMap) ValidateAttr(val cty.Value, attrPath cty.Path, diags *tfdiags.Diagnostics) {}

type validateSet struct {
	Validators []setValidator
}

func (v validateSet) ValidateAttr(val cty.Value, attrPath cty.Path, diags *tfdiags.Diagnostics) {
	for _, validator := range v.Validators {
		validator(val, attrPath, diags)
		if diags.HasErrors() {
			return
		}
	}
}

type schemaAttribute interface {
	SchemaAttribute() *configschema.Attribute
	Validator() validateSchema
}

type stringAttribute struct {
	configschema.Attribute
	validateString
}

func (a stringAttribute) SchemaAttribute() *configschema.Attribute {
	return &a.Attribute
}

func (a stringAttribute) Validator() validateSchema {
	return a.validateString
}

type setAttribute struct {
	configschema.Attribute
	validateSet
}

func (a setAttribute) SchemaAttribute() *configschema.Attribute {
	return &a.Attribute
}

func (a setAttribute) Validator() validateSchema {
	return a.validateSet
}

type mapAttribute struct {
	configschema.Attribute
	validateMap
}

func (a mapAttribute) SchemaAttribute() *configschema.Attribute {
	return &a.Attribute
}

func (a mapAttribute) Validator() validateSchema {
	return a.validateMap
}

type objectSchema map[string]schemaAttribute

func (s objectSchema) SchemaAttributes() map[string]*configschema.Attribute {
	m := make(map[string]*configschema.Attribute, len(s))
	for k, v := range s {
		m[k] = v.SchemaAttribute()
	}
	return m
}

func assumeRoleFullSchema() objectSchema {
	return map[string]schemaAttribute{
		"role_arn": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Required:    true,
				Description: "The role to be assumed.",
			},
			validateString{
				Validators: []stringValidator{
					validateARN(
						validateIAMRoleARN,
					),
				},
			},
		},

		"duration": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "The duration, between 15 minutes and 12 hours, of the role session. Valid time units are ns, us (or µs), ms, s, h, or m.",
			},
			validateString{
				Validators: []stringValidator{
					validateDuration(
						validateDurationBetween(15*time.Minute, 12*time.Hour),
					),
				},
			},
		},

		"external_id": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "The external ID to use when assuming the role",
			},
			validateString{
				Validators: []stringValidator{
					validateStringLenBetween(2, 1224),
					validateStringMatches(
						regexp.MustCompile(`^[\w+=,.@:\/\-]*$`),
						`Value can only contain letters, numbers, or the following characters: =,.@/-`,
					),
				},
			},
		},

		"policy": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "IAM Policy JSON describing further restricting permissions for the IAM Role being assumed.",
			},
			validateString{
				Validators: []stringValidator{
					validateStringNotEmpty,
					validateIAMPolicyDocument,
				},
			},
		},

		"policy_arns": setAttribute{
			configschema.Attribute{
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "Amazon Resource Names (ARNs) of IAM Policies describing further restricting permissions for the IAM Role being assumed.",
			},
			validateSet{
				Validators: []setValidator{
					validateSetStringElements(
						validateARN(
							validateIAMPolicyARN,
						),
					),
				},
			},
		},

		"session_name": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "The session name to use when assuming the role.",
			},
			validateString{
				Validators: []stringValidator{
					validateStringLenBetween(2, 64),
					validateStringMatches(
						regexp.MustCompile(`^[\w+=,.@\-]*$`),
						`Value can only contain letters, numbers, or the following characters: =,.@-`,
					),
				},
			},
		},

		// NOT SUPPORTED by `aws-sdk-go-base/v1`
		// "source_identity": stringAttribute{
		// 	configschema.Attribute{
		// 		Type:         cty.String,
		// 		Optional:     true,
		// 		Description:  "Source identity specified by the principal assuming the role.",
		// 		ValidateFunc: validAssumeRoleSourceIdentity,
		// 	},
		// },

		"tags": mapAttribute{
			configschema.Attribute{
				Type:        cty.Map(cty.String),
				Optional:    true,
				Description: "Assume role session tags.",
			},
			validateMap{},
		},

		"transitive_tag_keys": setAttribute{
			configschema.Attribute{
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "Assume role session tag keys to pass to any subsequent sessions.",
			},
			validateSet{},
		},
	}
}

func assumeRoleWithWebIdentityFullSchema() objectSchema {
	return map[string]schemaAttribute{
		"role_arn": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Required:    true,
				Description: "The role to be assumed.",
			},
			validateString{
				Validators: []stringValidator{
					validateARN(
						validateIAMRoleARN,
					),
				},
			},
		},

		"duration": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "The duration, between 15 minutes and 12 hours, of the role session. Valid time units are ns, us (or µs), ms, s, h, or m.",
			},
			validateString{
				Validators: []stringValidator{
					validateDuration(
						validateDurationBetween(15*time.Minute, 12*time.Hour),
					),
				},
			},
		},

		"policy": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "IAM Policy JSON describing further restricting permissions for the IAM Role being assumed.",
			},
			validateString{
				Validators: []stringValidator{
					validateStringNotEmpty,
					validateIAMPolicyDocument,
				},
			},
		},

		"policy_arns": setAttribute{
			configschema.Attribute{
				Type:        cty.Set(cty.String),
				Optional:    true,
				Description: "Amazon Resource Names (ARNs) of IAM Policies describing further restricting permissions for the IAM Role being assumed.",
			},
			validateSet{
				Validators: []setValidator{
					validateSetStringElements(
						validateARN(
							validateIAMPolicyARN,
						),
					),
				},
			},
		},

		"session_name": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "The session name to use when assuming the role.",
			},
			validateString{
				Validators: []stringValidator{
					validateStringLenBetween(2, 64),
					validateStringMatches(
						regexp.MustCompile(`^[\w+=,.@\-]*$`),
						`Value can only contain letters, numbers, or the following characters: =,.@-`,
					),
				},
			},
		},

		"web_identity_token": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "Value of a web identity token from an OpenID Connect (OIDC) or OAuth provider.",
			},
			validateString{
				Validators: []stringValidator{
					validateStringLenBetween(4, 20000),
				},
			},
		},

		"web_identity_token_file": stringAttribute{
			configschema.Attribute{
				Type:        cty.String,
				Optional:    true,
				Description: "File containing a web identity token from an OpenID Connect (OIDC) or OAuth provider.",
			},
			validateString{
				Validators: []stringValidator{
					validateStringLenBetween(4, 20000),
				},
			},
		},
	}
}

func deprecatedAttrDiag(attr, replacement cty.Path) tfdiags.Diagnostic {
	return attributeWarningDiag(
		"Deprecated Parameter",
		fmt.Sprintf(`The parameter "%s" is deprecated. Use parameter "%s" instead.`, pathString(attr), pathString(replacement)),
		attr,
	)
}

func deprecatedEnvVarDiag(envvar, replacement string) tfdiags.Diagnostic {
	return wholeBodyWarningDiag(
		"Deprecated Environment Variable",
		fmt.Sprintf(`The environment variable "%s" is deprecated. Use environment variable "%s" instead.`, envvar, replacement),
	)
}
