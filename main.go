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
	"time"

	"cloud.google.com/go/firestore"
	"github.com/slack-go/slack"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var verificationToken string

// Define a global variable to be reused in the next function call.
// reference:https://cloud.google.com/functions/docs/bestpractices/tips#use_global_variables_to_reuse_objects_in_future_invocations
func init() {
	verificationToken = os.Getenv("VERIFICATION_TOKEN")
}

// CloudFunction entry point.
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

		pointTransaction := pointTran{SenderId: slashCommand.UserID, RecieverId: recieverId, Reason: reason, Points: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := givePoint(ctx, client, pointTransaction); err != nil {
			http.Error(w, "Error giving point", http.StatusInternalServerError)
			return
		}

		point, err := saveUser(ctx, client, recieverId)
		if err != nil {
			http.Error(w, "Error saving point", http.StatusInternalServerError)
			return
		}

		params := &slack.Msg{
			ResponseType: "in_channel",
			Text:         fmt.Sprintf("<@%s>さんが<@%s>さんにイイねポイントを付与しました！ \n【付与理由】\n %s \n【獲得ポイント数】\n %v pt", slashCommand.UserID, recieverId, reason, point)}
		b, err := json.Marshal(params)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case "/show_goodpoint_daily":
		// TODO:
	case "/show_goodpoint_weekly":
		// TODO:
	case "/show_goodpoint_monthly":
		// TODO:
	case "/show_goodpoint_ranking":
		// TODO:
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

func saveUser(ctx context.Context, client *firestore.Client, userId string) (int, error) {
	ref := client.Collection("users").Doc(userId)
	dsnap, err := ref.Get(ctx)
	point := 0
	if err != nil {
		if status.Code(err) == codes.NotFound {
			point = 1
		} else {
			return 0, fmt.Errorf("failed to get user document: %v", err)
		}
	} else {
		data := dsnap.Data()
		p := data["points"]
		// The type of the points retrieved from Firestore is `int64`.
		pInt64, ok := p.(int64)
		if !ok {
			return 0, fmt.Errorf("failed to convert int64: %v", p)
		}
		point = int(pInt64) + 1
	}
	if _, err = ref.Set(ctx, map[string]interface{}{
		"points": point,
	}, firestore.MergeAll); err != nil {
		return 0, err
	}
	return point, nil
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

// point transaction
type pointTran struct {
	SenderId   string    `firestore:"sender_id"`
	RecieverId string    `firestore:"reciever_id"`
	Reason     string    `firestore:"reason"`
	Points     int       `firestore:"points"`
	CreatedAt  time.Time `firestore:"created_at"`
	UpdatedAt  time.Time `firestore:"updated_at"`
}
