// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const bedrockCloudWatchMaxQueries = 100

type cloudWatchGetMetricDataAPI interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

type bedrockMetricPrice struct {
	price float64
}

func newBedrockCloudWatchClient(ctx context.Context, entry ProviderUsageReconciliationEntry) (cloudWatchGetMetricDataAPI, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(strings.TrimSpace(entry.AWSRegion)))
	if err != nil {
		return nil, errors.New("Bedrock CloudWatch credentials are unavailable")
	}
	if role := strings.TrimSpace(entry.AWSRoleARN); role != "" {
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), role))
	}
	return cloudwatch.NewFromConfig(cfg), nil
}

func (c *providerUsageClient) bedrockCost(ctx context.Context, entry ProviderUsageReconciliationEntry, from, to time.Time) (float64, error) {
	if c.cloudWatchFactory == nil {
		return 0, errors.New("Bedrock CloudWatch client is unavailable")
	}
	provider, ok := c.providers[strings.TrimSpace(entry.Provider)]
	if !ok || len(provider.Models) == 0 {
		return 0, errors.New("Bedrock model pricing is unavailable")
	}
	modelNames := make([]string, 0, len(provider.Models))
	for name := range provider.Models {
		modelNames = append(modelNames, name)
	}
	sort.Strings(modelNames)
	queries := make([]types.MetricDataQuery, 0, len(modelNames)*2)
	prices := make(map[string]bedrockMetricPrice, len(modelNames)*2)
	seenModels := make(map[string]ProviderModel, len(modelNames))
	for index, name := range modelNames {
		model := provider.Models[name]
		modelID := strings.TrimSpace(model.Model)
		if modelID == "" {
			modelID = strings.TrimSpace(name)
		}
		if modelID == "" || !finiteNonNegative(model.InputPricePerMillionUSD) || !finiteNonNegative(model.OutputPricePerMillionUSD) {
			return 0, errors.New("Bedrock model pricing is invalid")
		}
		if previous, exists := seenModels[modelID]; exists {
			if previous.InputPricePerMillionUSD != model.InputPricePerMillionUSD || previous.OutputPricePerMillionUSD != model.OutputPricePerMillionUSD {
				return 0, errors.New("Bedrock model pricing is ambiguous")
			}
			continue
		}
		seenModels[modelID] = model
		for _, metric := range []struct {
			name  string
			price float64
		}{{"InputTokenCount", model.InputPricePerMillionUSD}, {"OutputTokenCount", model.OutputPricePerMillionUSD}} {
			id := "m" + strconv.Itoa(index) + map[string]string{"InputTokenCount": "i", "OutputTokenCount": "o"}[metric.name]
			queries = append(queries, types.MetricDataQuery{
				Id: aws.String(id),
				MetricStat: &types.MetricStat{
					Metric: &types.Metric{Namespace: aws.String("AWS/Bedrock"), MetricName: aws.String(metric.name), Dimensions: []types.Dimension{{Name: aws.String("ModelId"), Value: aws.String(modelID)}}},
					Period: aws.Int32(86400), Stat: aws.String("Sum"),
				},
				ReturnData: aws.Bool(true),
			})
			prices[id] = bedrockMetricPrice{price: metric.price}
		}
	}
	if len(queries) == 0 || len(queries) > bedrockCloudWatchMaxQueries {
		return 0, errors.New("Bedrock model count exceeds the bounded CloudWatch query limit")
	}
	client, err := c.cloudWatchFactory(ctx, entry)
	if err != nil {
		return 0, errors.New("Bedrock CloudWatch client is unavailable")
	}
	pageLimit := c.maxPages
	if pageLimit <= 0 {
		pageLimit = providerUsageDefaultPages
	}
	start, end := from.UTC(), to.UTC()
	var nextToken *string
	var total float64
	for page := 0; page < pageLimit; page++ {
		output, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			StartTime: &start, EndTime: &end, MetricDataQueries: queries, NextToken: nextToken,
			MaxDatapoints: aws.Int32(int32(len(queries))), ScanBy: types.ScanByTimestampAscending,
		})
		if err != nil || output == nil {
			return 0, errors.New("Bedrock CloudWatch query failed")
		}
		for _, result := range output.MetricDataResults {
			if result.StatusCode != types.StatusCodeComplete && result.StatusCode != types.StatusCodePartialData {
				return 0, errors.New("Bedrock CloudWatch returned incomplete metrics")
			}
			if result.StatusCode == types.StatusCodePartialData && output.NextToken == nil {
				return 0, errors.New("Bedrock CloudWatch returned incomplete metrics")
			}
			price, exists := prices[aws.ToString(result.Id)]
			if !exists {
				return 0, errors.New("Bedrock CloudWatch returned an unknown metric")
			}
			for _, tokens := range result.Values {
				if !finiteNonNegative(tokens) {
					return 0, errors.New("Bedrock CloudWatch returned an invalid token count")
				}
				cost := tokens * price.price / 1_000_000
				if !finiteNonNegative(cost) || !finiteNonNegative(total+cost) {
					return 0, errors.New("Bedrock CloudWatch returned an invalid cost")
				}
				total += cost
			}
		}
		nextToken = output.NextToken
		if nextToken == nil || strings.TrimSpace(*nextToken) == "" {
			return total, nil
		}
	}
	return 0, errors.New("Bedrock CloudWatch page limit exceeded")
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
