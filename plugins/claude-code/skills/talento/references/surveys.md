# Surveys

Write the questionnaire in the user's language. When the user has already given the questions, or the
NPS question, create the survey on that turn. Pass audience, frequency, notification, and activation
only when they have already chosen them. When the request is only a topic, propose the name, the
sections, and the actual questions, plus who should receive it and when a round opens. Create it after
they accept that proposal. Ask only about a choice that changes who is asked, when, or what is asked.

## Questions

One idea per question, in neutral wording. A rating is for a degree. A selection question is for real
categories. Keep optional text questions to one or two per section. Leave ratings on the default 1–5
scale. An NPS survey is the exception: one recommendation question, on the 0–10 scale the tool creates,
with the optional comment kept unless they decline it, and with no other sections. Field names and the
request shape are on `talento surveys create --agent --help`.

- A poll is one to three selection questions.
- Feedback is one topic, usually five to ten questions.
- A workplace climate or engagement questionnaire covers the parts they care about, such as workload,
  management, the team, growth, communication, or wellbeing. It is usually ten to twenty questions,
  mostly ratings, and anonymous unless they want answers attributed. Write those questions for their
  situation.

## The company pulse

Before creating anything the user calls a climate survey, list the climate surveys they manage. The
company's pulse is the first climate survey that was created. Change its schedule, audience, or
activation with `talento surveys update`. Creating another climate survey does not replace that pulse,
and opening a climate survey asks for the pulse vote instead of sending a new questionnaire. The CLI
cannot edit the pulse's question. A written climate or engagement questionnaire is a feedback survey.

## Who receives it, and when

Audience, frequency, notification, and activation are arguments of create and of update. Read the flags
from `talento surveys create --agent --help` and `talento surveys update --agent --help`.

With no audience, only the owner is a respondent. Passing an audience replaces the current one. That
audience can be the whole company, offices, teams, people, or a combination. Start collection only when
they ask to turn the survey on. That start does not email anyone unless they also want respondents
notified when a round opens.

After a successful create or update, report the name and the status Talento returned: who receives it,
the frequency, and whether it is collecting responses.
