package provider

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock/types"
)

func TestOwnerFromProfile(t *testing.T) {
	cases := []struct {
		name string
		arns []string
		want string
	}{
		{
			name: "anthropic via foundation-model arn",
			arns: []string{"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-sonnet-4-5-20250929-v1:0"},
			want: "Anthropic",
		},
		{
			name: "amazon nova",
			arns: []string{"arn:aws:bedrock:us-west-2::foundation-model/amazon.nova-pro-v1:0"},
			want: "Amazon",
		},
		{
			name: "skips a malformed leading entry, falls through to the next",
			arns: []string{"arn:aws:bedrock::missing-slash", "arn:aws:bedrock:us-east-1::foundation-model/meta.llama3-70b-instruct-v1:0"},
			want: "Meta",
		},
		{
			name: "no model arns",
			arns: nil,
			want: "",
		},
		{
			name: "arn with no provider segment",
			arns: []string{"arn:aws:bedrock:us-east-1::foundation-model/no-dot-here"},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			models := make([]types.InferenceProfileModel, 0, len(tc.arns))
			for _, a := range tc.arns {
				arn := a
				models = append(models, types.InferenceProfileModel{ModelArn: aws.String(arn)})
			}
			prof := types.InferenceProfileSummary{Models: models}
			if got := ownerFromProfile(prof); got != tc.want {
				t.Errorf("ownerFromProfile = %q, want %q", got, tc.want)
			}
		})
	}
}
