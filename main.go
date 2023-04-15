package slackapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/slack-go/slack"
)

var verificationToken string

// グローバル変数を定義して次回の関数呼び出し時にも再利用する。
// references:https://cloud.google.com/functions/docs/bestpractices/tips#use_global_variables_to_reuse_objects_in_future_invocations
func init() {
	verificationToken = os.Getenv("VERIFICATION_TOKEN")
}

// Entry point
func HandleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slashCommand, err := slack.SlashCommandParse(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !slashCommand.ValidateToken(verificationToken) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch slashCommand.Command {
	case "/hello":
		// ResponseType のデフォルトは ephemeral になっており、ephemeral　では、投稿者にしかメッセージが表示されない。
		// チャンネル全体に投稿する時は、in_channel を指定する。
		params := &slack.Msg{ResponseType: "in_channel", Text: "こんにちは、<@" + slashCommand.UserID + ">さん。GoodPointアプリへようこそ！"}
		b, err := json.Marshal(params)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case "/give_goodpoint":
		recieverId, reason, err := extractRecieverIdAndReason(slashCommand.Text)
		if err != nil || recieverId == "" || reason == "" {
			http.Error(w, "Invalid input", http.StatusBadRequest)
			return
		}
		ctx := context.Background()
		client, err := firestore.NewClient(ctx, "good-point-dev")
		if err != nil {
			http.Error(w, "Error creating Firestore client", http.StatusInternalServerError)
			return
		}
		defer client.Close()

		err = givePoint(ctx, client, pointTran{SenderId: slashCommand.UserID, RecieverId: recieverId, Reason: reason, Points: 1})
		if err != nil {
			http.Error(w, "Error giving point", http.StatusInternalServerError)
			return
		}

		params := &slack.Msg{ResponseType: "in_channel", Text: fmt.Sprintf("<@%s>さんが<@%s>さんにイイねポイントを付与しました！ 付与理由： %s.", slashCommand.UserID, recieverId, reason)}
		b, err := json.Marshal(params)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	default:
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func extractRecieverIdAndReason(text string) (string, string, error) {
	// example : test = "<@U123456789|firstname.lastname> reasons"
	re := regexp.MustCompile(`<@(U[A-Z0-9]+)\|[^>]+>`)
	matches := re.FindStringSubmatch(text)

	if len(matches) < 2 {
		return "", "", fmt.Errorf("failed to extract mention from text")
	}

	recieverId := matches[1]
	reason := strings.TrimSpace(strings.Replace(text, matches[0], "", 1))
	return recieverId, reason, nil
}

func givePoint(ctx context.Context, client *firestore.Client, tran pointTran) error {
	tranMap, err := toMap(tran)
	if err != nil {
		return err
	}
	_, _, err = client.Collection("pointTransactions").Add(ctx, tranMap)
	if err != nil {
		return err
	}
	return nil
}

func toMap(s interface{}) (map[string]interface{}, error) {
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

type pointTran struct {
	SenderId   string `firestore:"sender_id"`
	RecieverId string `firestore:"reciever_id"`
	Reason     string `firestore:"reason"`
	Points     int8   `firestore:"points"`
}
