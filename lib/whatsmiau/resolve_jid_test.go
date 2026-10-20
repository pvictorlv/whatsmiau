package whatsmiau

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func pn(user string) types.JID { return types.NewJID(user, types.DefaultUserServer) }
func lid(user string) types.JID { return types.NewJID(user, types.HiddenUserServer) }

// Regression: IsOnWhatsApp devolve o ID canônico em JID, que em contas com LID é
// `<id>@lid`. O código antigo copiava só o `.User` para um JID `@s.whatsapp.net`,
// gerando `<lid>@s.whatsapp.net`. O whatsmeow então tentava GetLIDForPN nesse
// falso telefone e o envio morria com "no LID found for ... from server".
func TestPickResolvedJID(t *testing.T) {
	original := pn("559192830619")

	tests := []struct {
		name string
		resp []types.IsOnWhatsAppResponse
		want types.JID
	}{
		{
			name: "canonical JID is a LID: uses PhoneNumber, never the LID user",
			resp: []types.IsOnWhatsAppResponse{{
				IsIn:        true,
				JID:         lid("247695209967637"),
				PhoneNumber: pn("559192830619"),
			}},
			want: pn("559192830619"),
		},
		{
			name: "brazilian alternate resolved: keeps the number the server confirmed",
			resp: []types.IsOnWhatsAppResponse{{
				IsIn:        true,
				JID:         lid("247695209967637"),
				PhoneNumber: pn("5591992830619"),
			}},
			want: pn("5591992830619"),
		},
		{
			name: "legacy response without PhoneNumber: JID is already the PN",
			resp: []types.IsOnWhatsAppResponse{{
				IsIn: true,
				JID:  pn("5591992830619"),
			}},
			want: pn("5591992830619"),
		},
		{
			name: "LID only, no PhoneNumber: sends to the LID",
			resp: []types.IsOnWhatsAppResponse{{
				IsIn: true,
				JID:  lid("247695209967637"),
			}},
			want: lid("247695209967637"),
		},
		{
			name: "LID with device part: drops the device",
			resp: []types.IsOnWhatsAppResponse{{
				IsIn: true,
				JID:  types.NewADJID("247695209967637", types.LIDDomain, 2),
			}},
			want: lid("247695209967637"),
		},
		{
			name: "registered but empty JID: keeps the original number",
			resp: []types.IsOnWhatsAppResponse{{IsIn: true, JID: types.EmptyJID}},
			want: original,
		},
		{
			name: "not registered: keeps the original number",
			resp: []types.IsOnWhatsAppResponse{{
				IsIn:        false,
				JID:         lid("247695209967637"),
				PhoneNumber: pn("5591992830619"),
			}},
			want: original,
		},
		{
			name: "skips the unregistered entry and takes the registered one",
			resp: []types.IsOnWhatsAppResponse{
				{IsIn: false, JID: pn("559192830619")},
				{IsIn: true, JID: lid("247695209967637"), PhoneNumber: pn("5591992830619")},
			},
			want: pn("5591992830619"),
		},
		{
			name: "empty response: keeps the original number",
			resp: nil,
			want: original,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickResolvedJID(original, tt.resp)

			if got != tt.want {
				t.Fatalf("pickResolvedJID() = %s, want %s", got, tt.want)
			}
			// O destino jamais pode ser um LID vestido de telefone.
			if got.Server == types.DefaultUserServer && len(got.User) > 15 {
				t.Fatalf("resolved JID looks like a LID on the phone server: %s", got)
			}
		})
	}
}

// Regression: a Unimed tem o fixo 11 5192-6625 e outra conta no mesmo número com
// o 9 (11 95192-6625). Quando as duas existem, a resposta pode trazer a variante
// com 9 primeiro; o envio tem que continuar indo para o número que o contato já
// usa, não para a "irmã".
func TestPickResolvedJIDPrefersTypedNumberWhenBothVariantsExist(t *testing.T) {
	original := pn("551151926625")

	resp := []types.IsOnWhatsAppResponse{
		{Query: "5511951926625", IsIn: true, JID: lid("111111111111111"), PhoneNumber: pn("5511951926625")},
		{Query: "551151926625", IsIn: true, JID: lid("222222222222222"), PhoneNumber: pn("551151926625")},
	}

	if got := pickResolvedJID(original, resp); got != original {
		t.Fatalf("pickResolvedJID() = %s, want %s", got, original)
	}
}

func TestPickResolvedJIDMovesToAlternateWhenTypedNumberIsNotRegistered(t *testing.T) {
	original := pn("559192830619")

	resp := []types.IsOnWhatsAppResponse{
		{Query: "559192830619", IsIn: false},
		{Query: "5591992830619", IsIn: true, JID: lid("247695209967637"), PhoneNumber: pn("5591992830619")},
	}

	want := pn("5591992830619")
	if got := pickResolvedJID(original, resp); got != want {
		t.Fatalf("pickResolvedJID() = %s, want %s", got, want)
	}
}
