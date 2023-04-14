package slackpoint

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"cloud.google.com/go/firestore"
)

func ExtractRecieverIdAndReason(text string) (string, string, error) {
	// example : test = "<@U123456789|firstname.lastname> reasons"
	re := regexp.MustCompile(`<@([A-Z0-9]+)\|[^>]+>`)
	matches := re.FindStringSubmatch(text)

	if len(matches) < 2 {
		return "", "", fmt.Errorf("failed to extract mention from text")
	}

	recieverId := matches[1]
	reason := strings.TrimSpace(strings.Replace(text, matches[0], "", 1))
	return recieverId, reason, nil
}

func GivePoint(ctx context.Context, client *firestore.Client, tran PointTran) error {
	tranMap, err := structToMap(tran)
	if err != nil {
		return err
	}
	_, _, err = client.Collection("pointTransactions").Add(ctx, tranMap)
	if err != nil {
		return err
	}
	return nil
}

func structToMap(s interface{}) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct, got %s", v.Kind())
	}

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i).Interface()
		result[field.Tag.Get("firestore")] = value
	}
	return result, nil
}
