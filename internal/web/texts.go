package web

// texts are the page strings; the script reads the dynamic ones from data attributes.
type texts struct {
	DefaultTitle, DefaultMessage                        string
	Starting, Step, ETA, Ready, Slow, Cap, Error, Sleep string
	Asleep, Button, Contact, Progress                   string
	// Setup lines, shown with page.show_setup.
	SetupTitle, IdleRule, WakeVisit, WakeButton, Ranges, NoRanges string
}

func textsFor(lang string) texts {
	if lang == "en" {
		return texts{
			DefaultTitle:   "Waking the service up",
			DefaultMessage: "This service went to sleep after a quiet spell. It is starting again, which usually takes one to two minutes.",
			Starting:       "Starting up…",
			Step:           "Step {n} of {total}",
			ETA:            "About {s} left",
			Ready:          "Ready, reloading…",
			Slow:           "This is taking longer than usual; the page reloads by itself.",
			Cap:            "Automatic wake-up is paused for now.",
			Error:          "Start-up ran into a problem.",
			Sleep:          "Going to sleep…",
			Asleep:         "The stack is asleep. Press Start to wake it up.",
			Button:         "Start",
			Contact:        "Contact the team",
			Progress:       "Start-up progress",
			SetupTitle:     "How this stack sleeps",
			IdleRule:       "Goes to sleep after %s without traffic",
			WakeVisit:      "Wakes up on the first visit",
			WakeButton:     "Wakes up with the Start button",
			Ranges:         "Always up %s (%s, %s)",
			NoRanges:       "No awake hours: on demand only",
		}
	}
	return texts{
		DefaultTitle:   "On rallume le service",
		DefaultMessage: "Ce service s'était mis en veille faute de visites. Il redémarre, cela prend en général une à deux minutes.",
		Starting:       "Démarrage en cours…",
		Step:           "Étape {n} sur {total}",
		ETA:            "Encore environ {s}",
		Ready:          "C'est prêt, rechargement…",
		Slow:           "C'est plus long que prévu ; la page se recharge toute seule.",
		Cap:            "Le réveil automatique est en pause pour le moment.",
		Error:          "Le démarrage a rencontré un problème.",
		Sleep:          "Mise en veille en cours…",
		Asleep:         "La stack est en pause. Clique sur Démarrer pour la réveiller.",
		Button:         "Démarrer",
		Contact:        "Contacter l'équipe",
		Progress:       "Progression du démarrage",
		SetupTitle:     "Comment cette stack se met en pause",
		IdleRule:       "Mise en pause après %s sans trafic",
		WakeVisit:      "Réveil à la première visite",
		WakeButton:     "Réveil avec le bouton Démarrer",
		Ranges:         "Toujours allumée %s (%s, %s)",
		NoRanges:       "Pas de plage horaire : réveil à la demande",
	}
}
