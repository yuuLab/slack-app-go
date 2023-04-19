package slackapp

import (
	"bytes"
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
	"google.golang.org/api/iterator"
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

	message := ""
	// Create firebase client.
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "good-point-dev")

	if err != nil {
		http.Error(w, "Error creating Firestore client", http.StatusInternalServerError)
		return
	}
	defer client.Close()

	switch slashCommand.Command {
	case "/hello":
		message = handleHello(slashCommand)
	case "/give_goodpoint":
		pointTrans, err := extractPointTransaction(slashCommand)
		if err != nil || pointTrans.RecieverId == "" || pointTrans.Reason == "" {
			http.Error(w, "Invalid input", http.StatusBadRequest)
			return
		}
		if err := handleGiveGoodPoint(ctx, client, pointTrans); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		message = fmt.Sprintf(
			"<@%s>さんが<@%s>さんにイイねポイントを付与しました！ \n\n【付与理由】\n %s \n【獲得ポイント数】\n %v pt",
			slashCommand.UserID, pointTrans.RecieverId, pointTrans.Reason, pointTrans.Points+1)
	case "/show_goodpoint_monthly_history":
		pointTrans, keys, err := inquirePointTran(ctx, client, startOfMonth())
		if err != nil {
			http.Error(w, "Error inquire monthly goddpoint transaction ", http.StatusInternalServerError)
			return
		}
		var bf bytes.Buffer
		bf.WriteString("*****月間付与履歴*****\n")
		for _, key := range keys {
			tx := pointTrans[key]
			bf.WriteString(fmt.Sprintf("%s  <@%s>さんから<@%s>さんへ付与。『%v』 （付与ID = %s）\n", toYyyymmdd(tx.CreatedAt), tx.SenderId, tx.RecieverId, tx.Reason, key))
		}
		message = bf.String()
	case "/show_goodpoint_ranking":
		limit := 10
		rankings, err := inquireRanking(ctx, client, limit)
		if err != nil {
			http.Error(w, "Error inquire goddpoint ranking", http.StatusInternalServerError)
			return
		}
		var bf bytes.Buffer
		bf.WriteString("ランキング結果発表～～！\n\n")
		for i, ranking := range rankings {
			bf.WriteString(fmt.Sprintf("第%v位・・・<@%s>さん【獲得ポイント】%v pt \n", i+1, ranking.UserId, ranking.Points))
		}
		bf.WriteString("\n\n")
		bf.WriteString("いつもありがとうございます！皆さん拍手をお送りください👏👏")
		message = bf.String()
	default:
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// The default value for `ResponseType` is `ephemeral`, which means that the message will only be visible to the person who posted it.
	// To post a message to the entire channel, specify `in_channel`.
	params := &slack.Msg{
		ResponseType: "in_channel",
		Text:         message,
	}
	b, err := json.Marshal(params)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func handleHello(slashCommand slack.SlashCommand) string {
	return fmt.Sprintf("こんにちは、<@%s>さん。GoodPointアプリへようこそ！", slashCommand.UserID)
}

func handleGiveGoodPoint(ctx context.Context, client *firestore.Client, pointTrans pointTran) error {
	// grant 1 pt to target user.
	if err := givePoint(ctx, client, pointTrans); err != nil {
		return fmt.Errorf("failed to give point")
	}
	if err := saveUser(ctx, client, pointTrans.RecieverId); err != nil {
		return fmt.Errorf("failed to save point")
	}
	return nil
}

func inquireRanking(ctx context.Context, client *firestore.Client, limit int) ([]users, error) {
	query := client.Collection("users").OrderBy("points", firestore.Desc).Limit(limit)
	iter := query.Documents(ctx)
	result := []users{}
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		data := doc.Data()
		p := data["points"]
		// The type of the points retrieved from Firestore is `int64`.
		pInt64, ok := p.(int64)
		if !ok {
			return []users{}, fmt.Errorf("failed to convert int64: %v", p)
		}
		result = append(result, users{UserId: doc.Ref.ID, Points: int(pInt64)})
	}
	return result, nil
}

func inquirePointTran(ctx context.Context, client *firestore.Client, start time.Time) (pointTrans map[string]pointTran, sortedkeys []string, err error) {
	query := client.Collection("pointTransactions").Where("created_at", ">=", start).OrderBy("created_at", firestore.Desc)
	iter := query.Documents(ctx)
	result := map[string]pointTran{}
	sortedKeys := []string{}
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		var ptx pointTran
		doc.DataTo(&ptx)
		result[doc.Ref.ID] = ptx
		sortedKeys = append(sortedKeys, doc.Ref.ID)
	}
	return result, sortedKeys, nil
}

func extractPointTransaction(slashCommand slack.SlashCommand) (pointTran, error) {
	// example:slashCommand.Text="<@U123456789|firstname.lastname> reasons"
	re := regexp.MustCompile(`<@(U[A-Z0-9]+)\|[^>]+>`)
	matches := re.FindStringSubmatch(slashCommand.Text)

	if len(matches) < 2 {
		return pointTran{}, fmt.Errorf("failed to extract mention from text")
	}

	recieverId := matches[1]
	reason := strings.TrimSpace(strings.Replace(slashCommand.Text, matches[0], "", 1))
	return pointTran{SenderId: slashCommand.UserID, RecieverId: recieverId, Reason: reason, Points: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
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

func saveUser(ctx context.Context, client *firestore.Client, userId string) error {
	ref := client.Collection("users").Doc(userId)
	dsnap, err := ref.Get(ctx)
	point := 0
	if err != nil {
		if status.Code(err) == codes.NotFound {
			point = 1
		} else {
			return fmt.Errorf("failed to get user document: %v", err)
		}
	} else {
		data := dsnap.Data()
		p := data["points"]
		// The type of the points retrieved from Firestore is `int64`.
		pInt64, ok := p.(int64)
		if !ok {
			return fmt.Errorf("failed to convert int64: %v", p)
		}
		point = int(pInt64) + 1
	}
	if _, err = ref.Set(ctx, map[string]interface{}{
		"points": point,
	}, firestore.MergeAll); err != nil {
		return err
	}
	return nil
}

// Conver a struct into map.
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

// Get the date and time of the first day of the current month, based on the system date and time.
func startOfMonth() time.Time {
	now := time.Now()
	year, month, _ := now.Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, now.Location())
}

// Convert into `yyyy/mm/dd`
func toYyyymmdd(t time.Time) string {
	return t.Format("2006/01/02")
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

type users struct {
	UserId string
	Points int
}
