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

const (
	COMMAND_HELP         string = "/help_goodpoint"
	COMMAND_GIVE         string = "/give_goodpoint"
	COMMAND_SHOW_HISTORY string = "/show_goodpoint_monthly_history"
	COMMAND_SHOW_RANKING string = "/show_goodpoint_ranking"
	COMMAND_DELETE       string = "/delete_goodpoint"
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
	case COMMAND_HELP:
		message = handleHelpGoodpoint(slashCommand)
	case COMMAND_GIVE:
		pointTrans, err := extractPointTransaction(slashCommand)
		if err != nil || pointTrans.RecieverId == "" || pointTrans.Reason == "" {
			http.Error(w, "Invalid input", http.StatusBadRequest)
			return
		}
		ms, err := handleGiveGoodPoint(ctx, client, pointTrans)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		message = ms
	case COMMAND_SHOW_HISTORY:
		ms, err := handleShowGoodpointHistory(ctx, client, startOfMonth())
		if err != nil {
			http.Error(w, "Error inquire monthly goddpoint transaction ", http.StatusInternalServerError)
			return
		}
		message = ms
	case COMMAND_SHOW_RANKING:
		limit := 10
		ms, err := handleShowRanking(ctx, client, limit)
		if err != nil {
			http.Error(w, "Error inquire goddpoint ranking", http.StatusInternalServerError)
			return
		}
		message = ms
	case COMMAND_DELETE:
		ms, isInvalid, err := handleDeleteGoodPoint(ctx, client, slashCommand)
		if err != nil {
			http.Error(w, "Error inquire goddpoint ranking", http.StatusInternalServerError)
			return
		}
		if isInvalid {
			// slashcommandの値不正
			http.Error(w, "Invalid input", http.StatusBadRequest)
			return
		}
		message = ms
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

// handle shash command `/hello_goodpoint`
func handleHelpGoodpoint(slashCommand slack.SlashCommand) string {
	var bf bytes.Buffer
	bf.WriteString("`/help`    使用可能なコマンド一覧表示\n")
	bf.WriteString("`/give_goodpoint @someone {give reasone}`  @someoneに対するいいねポイントの付与と理由。対象ユーザーに1ポイント付与されます。\n")
	bf.WriteString("`/show_goodpoint_monthly_history`  当月のイイねポイント付与履歴一覧表示\n")
	bf.WriteString("`/show_goodpoint_ranking`  これまでの総獲得いいねポイントのランキング表示\n")
	return bf.String()
}

// handle shash command `/give_goodpoint`
func handleGiveGoodPoint(ctx context.Context, client *firestore.Client, pointTrans pointTran) (string, error) {
	// grant 1 pt to target user.
	if err := savePointTransaction(ctx, client, pointTrans); err != nil {
		return "", fmt.Errorf("failed to give point")
	}
	point, err := saveUserForGrant(ctx, client, pointTrans.RecieverId)
	if err != nil {
		return "", fmt.Errorf("failed to save point")
	}
	return fmt.Sprintf(
		"<@%s>さんが<@%s>さんにイイねポイントを付与しました！ \n\n【付与理由】\n %s \n【獲得ポイント数】\n %v pt",
		pointTrans.SenderId, pointTrans.RecieverId, pointTrans.Reason, point), nil
}

// handle slashcommand `/delete_goodpoint`
func handleDeleteGoodPoint(ctx context.Context, client *firestore.Client, slashCommand slack.SlashCommand) (message string, isExisted bool, err error) {
	isInvalidId := false
	var targetTran pointTran
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// 削除対象の取得
		ptxRef := client.Collection("pointTransactions").Doc(slashCommand.Text)
		ptxDocSnap, err := tx.Get(ptxRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				isInvalidId = false
				return nil
			} else {
				return fmt.Errorf("failed to get pointTransaction document: %v", err)
			}
		}
		if err := ptxDocSnap.DataTo(&targetTran); err != nil {
			return err
		}
		// 削除
		if err := deleteDocument(ctx, client, "pointTransactions", slashCommand.Text); err != nil {
			return err
		}
		// 削除対象のユーザーのポイント数を更新
		userRef := client.Collection("users").Doc(targetTran.RecieverId)
		userDocSnap, err := tx.Get(userRef)
		if err != nil {
			return fmt.Errorf("failed to get a user document when deleting a point transaction: %v", err)
		}
		var user user
		if err := userDocSnap.DataTo(&user); err != nil {
			return err
		}
		return createOrUpdateUser(ctx, userRef, user.Points-targetTran.Points)
	})

	return fmt.Sprintf(
		"<@%s>さんが<@%s>さんへの付与を取り消しました。 \n\n【取消対象の付与理由】\n %s",
		slashCommand.UserID, targetTran.RecieverId, targetTran.Reason), isInvalidId, err
}

func deleteDocument(ctx context.Context, client *firestore.Client, collectionName, documentID string) error {
	docRef := client.Collection(collectionName).Doc(documentID)
	_, err := docRef.Delete(ctx)
	if err != nil {
		return err
	}
	return nil
}

func handleShowGoodpointHistory(ctx context.Context, client *firestore.Client, start time.Time) (string, error) {
	var bf bytes.Buffer
	bf.WriteString("*****月間付与履歴*****\n")
	// get pointTransactions from firestore
	query := client.Collection("pointTransactions").Where("created_at", ">=", start).OrderBy("created_at", firestore.Desc)
	iter := query.Documents(ctx)

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return "", err
		}
		var ptx pointTran
		doc.DataTo(&ptx)
		// add message
		bf.WriteString(fmt.Sprintf("%s  <@%s>さんから<@%s>さんへ付与。『%v』 （付与ID = %s）\n",
			toYyyymmdd(ptx.CreatedAt), ptx.SenderId, ptx.RecieverId, ptx.Reason, doc.Ref.ID))
	}
	return bf.String(), nil
}

func handleShowRanking(ctx context.Context, client *firestore.Client, limit int) (string, error) {
	// get users from firestore
	query := client.Collection("users").OrderBy("points", firestore.Desc).Limit(limit)
	iter := query.Documents(ctx)

	var bf bytes.Buffer
	bf.WriteString("ランキング結果発表！\n\n")

	var i int
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return "", err
		}
		var user user
		doc.DataTo(&user)
		// add message
		bf.WriteString(fmt.Sprintf("第%v位・・・<@%s>さん【獲得ポイント】%v pt \n", i+1, doc.Ref.ID, user.Points))
		i++
	}

	bf.WriteString("\n\n")
	bf.WriteString("いつもありがとうございます！皆さん拍手をお送りください👏👏")
	return bf.String(), nil
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

func savePointTransaction(ctx context.Context, client *firestore.Client, tran pointTran) error {
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

func saveUserForGrant(ctx context.Context, client *firestore.Client, userId string) (points int, err error) {

	// 合計ポイント更新のためトランザクションを制御する
	totalPoints := 0
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ref := client.Collection("users").Doc(userId)
		dsnap, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				// 存在しない場合新規作成
				totalPoints = 1
				return createOrUpdateUser(ctx, ref, totalPoints)
			} else {
				return fmt.Errorf("failed to get user document: %v", err)
			}
		}
		var user user
		if err := dsnap.DataTo(&user); err != nil {
			return err
		}
		totalPoints = user.Points + 1
		return createOrUpdateUser(ctx, ref, totalPoints)
	})
	if err != nil {
		return -1, err
	}
	return totalPoints, nil
}

func createOrUpdateUser(ctx context.Context, ref *firestore.DocumentRef, point int) error {
	_, err := ref.Set(ctx, map[string]interface{}{
		"points": point,
	}, firestore.MergeAll)
	return err
}

// Convert a struct into map.
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

type user struct {
	Points int `firestore:"points"`
}
