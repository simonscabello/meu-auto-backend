-- Every item the app can be asked about gets the wording to ask with.
--
-- Migration 000010 wrote a pt-BR history question for fourteen catalogue items and
-- left six of them NULL. The app then filtered those six out of the history flow --
-- correctly, because it invents no wording of its own -- while the "falta informar"
-- count still included them. The result was a screen saying three items were missing
-- their history and a flow that opened with one question.
--
-- These six are ordinary services with an ordinary answer: someone knows roughly when
-- the shock absorbers were last changed. Nothing about the model changes here; the six
-- items simply stop being unaskable.
--
-- Still deliberately NULL, and each for a reason the count now respects:
--
--   corrente_comando, bateria_tracao  inspection items with no replacement interval.
--                                     "When was it last replaced" is the wrong
--                                     question for something that is looked at.
--   personalizada                     the escape hatch. It groups history and has no
--                                     identity to ask about.
--   calibrar_pneus, lavar_carro,      habits. The last time someone washed the car is
--   verificar_*                       not a baseline anybody needs from an interview.
UPDATE maintenance_items SET history_question = v.question
FROM (VALUES
    ('alinhamento',    'Quando foi o último alinhamento?'),
    ('balanceamento',  'Quando foi o último balanceamento?'),
    ('rodizio_pneus',  'Quando foi o último rodízio dos pneus?'),
    ('palhetas',       'Quando as palhetas do limpador foram trocadas?'),
    ('discos_freio',   'Quando os discos de freio foram trocados?'),
    ('amortecedores',  'Quando os amortecedores foram trocados?')
) AS v(slug, question)
WHERE maintenance_items.slug = v.slug
  AND maintenance_items.owner_user_id IS NULL
  AND maintenance_items.history_question IS NULL;
