package function

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/slack-go/slack"
)

var verificationToken string

// グローバル変数を定義して次回の関数呼び出し時にも再利用する。
// references:https://cloud.google.com/functions/docs/bestpractices/tips#use_global_variables_to_reuse_objects_in_future_invocations
func init() {
	verificationToken = os.Getenv("VERIFICATION_TOKEN")
}

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
		params := &slack.Msg{ResponseType: "in_channel", Text: "只今メンテナンス中です...."}
		b, err := json.Marshal(params)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		// mention, reason, err := slackpoint.ExtractMentionAndReason(slashCommand.Text)
		// if err != nil || mention == "" || reason == "" {
		// 	http.Error(w, "Invalid input", http.StatusBadRequest)
		// 	return
		// }
		// ctx := context.Background()
		// client, err := firestore.NewClient(ctx, "good-point-dev")
		// if err != nil {
		// 	http.Error(w, "Error creating Firestore client", http.StatusInternalServerError)
		// 	return
		// }
		// defer client.Close()

		// err = slackpoint.GivePoint(ctx, client, slackpoint.SlackRequest{Sender: slashCommand.UserID, Reciever: mention, Reason: reason, Points: 1})
		// if err != nil {
		// 	http.Error(w, "Error giving point", http.StatusInternalServerError)
		// 	return
		// }

		// response := fmt.Sprintf("Successfully gave 1 point to <@%s> for %s.", mention, reason)
		// json.NewEncoder(w).Encode(map[string]string{"text": response})
	default:
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}
