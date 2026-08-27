# Meu Auto — Backend: Especificação Técnica e de Produto

> Resultado da sessão de brainstorming. Base para implementação.
> Verdade de produto: [`../meu-auto-app/PRODUCT.md`](../meu-auto-app/PRODUCT.md).
> Este documento cobre **apenas o backend** (`meu-auto-backend`, repositório independente).

---

## 1. Visão final do MVP

### Problema

Um dono de carro no Brasil não tem onde guardar o que aconteceu com o veículo dele. O
conhecimento fica na memória, em notas fiscais no porta-luvas e na palavra do mecânico.
Ele perde prazo, refaz serviço que já foi feito, não sabe o que o carro custa, e na hora
de vender não consegue provar nada.

### Core domain

> **A linha do tempo do veículo — eventos ancorados em (data, odômetro) — e o motor que
> deriva dela o que está vencido, o que vence em breve, e quanto custou.**

Consequências:

- **Odômetro + data são o par de coordenadas do domínio inteiro.** Todo evento relevante
  carrega os dois. É isso que permite responder "quantos km durou esse jogo de pneus"
  sem entidade nova.
- O que é genuinamente difícil: o **motor de vencimento** (km OU tempo, com baseline
  incerto) e a **separação histórico-do-veículo × dados-privados-do-dono**. É onde vale
  investir modelagem.
- O resto — CRUD de despesa, seguro, obrigação — é subdomínio de suporte. Não merece
  arquitetura sofisticada.

### Escopo do MVP-1

| # | Capacidade |
|---|---|
| 1 | Autenticação e conta (e-mail + senha) |
| 2 | Múltiplos veículos por conta (somente carro) |
| 3 | Histórico de odômetro |
| 4 | Manutenção realizada (cabeçalho + linhas de itens) |
| 5 | Planos de manutenção preventiva (km / tempo / ambos / sem periodicidade) |
| 6 | Cálculo de vencimentos (vencido / vence em breve / em dia) |
| 7 | Prazos com data fixa: IPVA, licenciamento, seguro |
| 8 | Dashboard do veículo (uma request) |
| 9 | Lembretes recorrentes de cuidado — **reusando o motor de planos**, sem entidade nova |

### Fora do MVP-1 (e por quê)

| Item | Motivo |
|---|---|
| Abastecimento | Feature de retenção semanal, mas ~40% de escopo. Primeiro item do MVP-2. |
| Despesas avulsas | Sem combustível, o relatório de custo estaria incompleto de qualquer forma. |
| Anexos / fotos | **Única infra nova do projeto** (object storage). MVP-1 = só Postgres. |
| Push notification | O app faz pull do dashboard. Nenhuma tabela de notificação agora. |
| Transferência de histórico | Só garantimos que a modelagem não impeça. |
| FIPE, SENATRAN, DETRAN | Nenhuma integração externa no MVP. |

> **O dashboard do MVP-1 não deve prometer "custo total do veículo".** Sem combustível,
> esse número está errado por ~60%. Ele mostra "custo de manutenção e obrigações".

---

## 2. Regras de negócio definidas

### RN-01 — Fonte da verdade da quilometragem

- A fonte da verdade é a tabela **`odometer_readings`** (append-only).
- `vehicles.current_mileage_km` e `current_mileage_at` são **cache denormalizado**,
  recalculados na mesma transação de qualquer escrita que gere leitura.
- A leitura "atual" é a de **maior `occurred_on`** (desempate: `created_at` mais recente).
  **Não usar `MAX(mileage_km)`** — isso impede correção de digitação e quebra em troca
  de painel.
- **A validação compara com os VIZINHOS NO TEMPO, não com a quilometragem atual.**
  Refinamento feito na implementação: comparar com o "atual" rejeitaria um lançamento
  retroativo legítimo — quem registra hoje uma leitura de três meses atrás está
  corretamente informando um número menor. O que é de fato inválido é uma leitura que
  contradiz os registros ao redor dela:

  ```
  leitura_anterior.km <= nova.km <= leitura_posterior.km
  ```

  Ausência de vizinho de qualquer lado simplesmente não restringe aquele lado.
- Violação responde `422` com código `odometer_rollback` e os detalhes do vizinho que
  causou o conflito. O cliente pode reenviar com `source: "correction"` para forçar.
  **Não bloquear duro** — painel trocado existe.
- **`source` aceito do cliente é só `manual` ou `correction`.** `maintenance` e
  `abastecimento` são escritos por aqueles módulos; aceitá-los aqui deixaria o cliente
  forjar uma leitura que se diz originada de uma manutenção.
- **Todo evento que informa km gera uma `odometer_reading`** na mesma transação, com
  `source` e FK tipada `ON DELETE CASCADE`. Apagou a manutenção, a leitura some junto.
  **Exceção:** um registro cujas linhas são todas `kind = care` pode omitir `mileage_km`.
  Sem km não há leitura: o hábito tem data, não odômetro. Inventar a quilometragem em
  cache fabricaria um fato (RN-03).

### RN-02 — Motor de vencimento (o coração do produto)

Função **pura** em Go. Sem I/O, sem banco, sem relógio global — `hoje` entra por parâmetro.

```
para cada plano ativo do veículo:
  last = último maintenance_record_item daquele item no veículo (occurred_on DESC)

  se plano.interval_km IS NULL e plano.interval_months IS NULL:
      status = SEM_PERIODICIDADE        # só agrupa histórico, nunca vence
  senão se last não existe:
      status = SEM_BASELINE             # o app pede a última troca ao usuário
  senão:
      due_km   = last.mileage_km  + plano.interval_km        (se definido)
      due_date = last.occurred_on + plano.interval_months    (se definido)
      rem_km   = due_km   - veiculo.current_mileage_km
      rem_days = due_date - hoje

      status = VENCIDO         se rem_km <= 0        OU rem_days <= 0
               VENCE_EM_BREVE  se rem_km <= alert_km OU rem_days <= alert_days
               EM_DIA          caso contrário
```

- **Semântica "OU" confirmada:** basta um dos limites ser atingido. O status é o **pior**
  entre a avaliação por km e a avaliação por tempo.
- Quando só um dos intervalos está definido, o outro simplesmente não participa.
- **Último registro sem `mileage_km`:** a dimensão de distância daquele plano não é
  avaliada — `due_at_km` e `remaining_km` ficam nulos e o status por distância permanece
  `em_dia` (neutro), exatamente como um plano sem `interval_km`. A guarda existe porque o
  dono pode acrescentar `interval_km` a um plano de cuidado; sem ela o cálculo usaria
  zero. Um cuidado sem km é `interval_days` puro.
- **Três dimensões de intervalo, não duas:** `interval_km`, `interval_months` e
  `interval_days`. `interval_days` foi adicionado na implementação porque um hábito é "a
  cada 15 dias" e uma revisão é "a cada 12 meses" — expressar um na unidade do outro está
  errado: 12 meses significa a **mesma data** no ano seguinte, não 365 dias. Sem ele, a
  unificação da RN-06 não fecha.
- **Soma de meses com clamp de fim de mês.** `AddDate` do Go normaliza 31/jan + 1 mês para
  03/mar, o que faz o aniversário da manutenção derivar a cada período. A implementação
  fixa no último dia do mês: 31/jan + 1 mês = 28/fev (29 em ano bissexto).
- **`alert_km`/`alert_days` são derivados do intervalo, não fixos.** Um alerta fixo de 500
  km é ruído numa correia de 60.000 km e inútil num óleo de 10.000. Um décimo do
  intervalo, com clamp (100–1000 km, 1–30 dias). O clamp importa mais no extremo curto:
  "calibrar a cada 15 dias" com aviso de 15 dias estaria permanentemente vencendo.
- **Um registro exige ao menos um item.** Registro sem item não reseta relógio nenhum e não
  pertence a plano nenhum — seria um custo sem significado para o motor. O item
  `personalizada` do catálogo é a saída para o que não tem nome.
- **"Próxima manutenção" nunca é armazenada.** Sempre calculada.
  Gatilho para denormalizar `next_due_km/date` no plano: quando o push notification
  precisar varrer todos os veículos em batch.

### RN-03 — Carro comprado usado / baseline sem registro

Não existem campos `last_performed_*` no plano. O usuário registra um
`maintenance_record` com `kind = 'declared'` (sem oficina, sem valor obrigatório).

**Um único caminho de cálculo**, e o histórico já nasce preenchido.

Um registro afirma data e quilometragem. Quem não tem as duas não deve gravá-las mesmo
assim. A exceção estrutural é o cuidado (`maintenance_items.kind = 'care'`): calibrar
pneu não produz km, então `maintenance_records.mileage_km` é nulável **somente** quando
todas as linhas do registro são `care`. Qualquer linha `maintenance` continua exigindo
km. Sem km, não se cria `odometer_reading` e o motor ignora a dimensão de distância.

**Nulidade consciente (D-01).** `mileage_km` deixa de ser sempre presente na resposta.
Um app antigo que faça `json['mileage_km'] as int` lança quando o valor é nulo. A janela
é segura enquanto só o app publicado grava registros — ele sempre manda km. O nulo só
aparece depois que o cliente que omite km (Fase 7 do app) for publicado. Campo novo
`care` na timeline; `kind` permanece `manutencao`.

### RN-04 — O dinheiro mora no evento que o gerou

Não existe tabela-razão central. Cada evento carrega seu próprio valor:
`maintenance_records.total_cost_cents`, `vehicle_obligations.paid_amount_cents`,
`seguros.premium_cents`, `abastecimentos.total_cost_cents` (MVP-2),
`expenses.amount_cents` (MVP-2).

**A duplicidade é impedida por constraint, não por disciplina:**

```sql
-- em expenses
CHECK (category IN ('estacionamento','pedagio','lavagem','multa','outros'))
```

`expenses` **só** aceita categorias que não têm evento próprio. É estruturalmente
impossível lançar a mesma manutenção, abastecimento, seguro, IPVA ou licenciamento
duas vezes.

O relatório de custo é uma **VIEW** `vehicle_costs` que faz `UNION ALL` das fontes,
normalizando `(vehicle_id, occurred_on, category, amount_cents, source_type, source_id)`.

> Gatilho para migrar para um ledger real: parcelamento, rateio entre pessoas, ou
> anexo de NF de forma uniforme.

### RN-05 — Garantia sem entidade nova

Nem `warranty`, nem `vehicle_part` no MVP.

- Garantia são **campos** em `maintenance_record_items`: `warranty_months`, `warranty_km`.
- `warranty_until` **não é armazenado** — é derivado de `record.occurred_on + warranty_months`.
  Consistente com "se dá para derivar, não armazene".
- **Durabilidade de componente** ("quantos km durou o jogo de pneus?") = delta entre
  registros **consecutivos do mesmo item**, via window function. Funciona igual para
  pneu, bateria, correia, pastilha, sem nenhuma entidade de ciclo de vida.

```sql
SELECT mileage_km - LAG(mileage_km) OVER (ORDER BY occurred_on) AS duracao_km
FROM ... WHERE maintenance_item_id = $1 AND vehicle_id = $2
```

> Gatilho para criar `vehicle_components`: quando precisar de **posição**
> (dianteiro/traseiro), **troca parcial** (2 dos 4 pneus) ou **rodízio**. Aí a
> sequência linear quebra de verdade. Não antes.

### RN-06 — Lembrete armazenado × derivado

Dois grupos, e o segundo já existe:

- **Derivados — nunca armazenados.** Próxima manutenção, fim de garantia, vencimento de
  IPVA/licenciamento/seguro. Tudo calculado sob demanda em `GET /v1/vehicles/{id}/alerts`.
  **Regra: se dá para derivar, não armazene.**
- **Hábitos recorrentes** (calibrar pneu a cada 15 dias, lavar carro, verificar óleo):
  estruturalmente **idênticos** a um plano só com intervalo de tempo. Usam o **mesmo
  motor**, com `maintenance_items.kind = 'care'` para o app separar na UI e a exportação
  de histórico filtrar. **Zero código novo, zero entidade nova.**
- **Não existe entidade `reminder` genérica.** Alinhado ao Princípio 4 do `PRODUCT.md`.

### RN-06b — Status de prazo também é derivado

Mesma regra da RN-06, aplicada a IPVA, licenciamento e seguro: **nenhum status é
armazenado**. Uma coluna `status` estaria errada na manhã seguinte à escrita.

| Obrigação (IPVA / licenciamento) | Seguro |
|---|---|
| `pago` — pagamento registrado, independente da data | `futuro` — contratado, ainda não vigente |
| `vencido` — passou do vencimento e não pago | `vigente` — em vigor, com folga |
| `vence_em_breve` — dentro de 30 dias | `vence_em_breve` — vence dentro de 30 dias |
| `pendente` — falta mais de 30 dias | `vencido` — período acabou, o carro está sem cobertura |

Dois detalhes que a implementação fixou:

- **Vencendo hoje é `vence_em_breve`, não `vencido`.** Ainda há horas para pagar, e dizer
  que a pessoa perdeu um prazo que ela não perdeu é pior que dizer que está perto.
- **Pagamento quita, mesmo em atraso.** IPVA pago depois do vencimento é `pago`, não
  `vencido` — a dívida acabou. Os dias restantes continuam sendo devolvidos (negativos), o
  que permite a tela mostrar "pago com 3 dias de atraso".

Janela de alerta: **30 dias** para todos. IPVA, licenciamento e renovação de apólice são
anuais, e um mês é aproximadamente o que se leva para juntar o dinheiro ou cotar.

### RN-07 — Autorização por ownership

- `vehicles` **não tem `user_id`**. O vínculo vive em `vehicle_ownerships`.
- **Nenhum handler toca um `vehicle_id` sem passar por `authorizeVehicleAccess()`.**
- Acesso negado responde **`404`, não `403`** — não vazar a existência do recurso.

### RN-08 — Sem unicidade de placa ou chassi no MVP-1

`plate` e `chassis` **não** têm `UNIQUE`. Dois usuários podem cadastrar a mesma placa.

Isso é deliberado e é uma decisão de **segurança**: sem verificação de propriedade, um
`UNIQUE` transformaria um erro de digitação em acesso ao veículo de outra pessoa.
A deduplicação (um chassi = um veículo) só entra junto com o fluxo de transferência,
que terá verificação. Índices não-únicos ficam prontos para essa busca futura.

### RN-09 — Planos padrão no cadastro do veículo

Ao criar um veículo, o backend materializa automaticamente os planos sugeridos do
catálogo para aquele `vehicle_type`, com `origin = 'suggested'`. Todos editáveis e
desativáveis. O usuário vê valor na primeira tela, sem configurar nada.

O flag `origin` permite, no futuro, atualizar intervalos sugeridos sem sobrescrever o
que o usuário customizou.

> **Refinado pela RN-11.** `suggest_by_default` diz que o item vale ser oferecido; **não**
> diz que o veículo tem o componente. Quem decide isso é o veículo.

### RN-11 — Aplicabilidade: o veículo define o contexto

Não existe plano universal de manutenção. Um carro elétrico não tem óleo de motor, um
diesel não tem vela, e nada disso é opinião — é o que as palavras significam.

**Não existe tabela de perfil.** `maintenance_plans` já é a linha que liga um veículo a um
item do catálogo, com intervalo e origem. Ela **é** o perfil. O que faltava nela:

| coluna | o que responde |
|---|---|
| `maintenance_plans.strategy` | `periodic` \| `inspection` \| `condition_based` \| `no_schedule` \| `not_applicable` |
| `maintenance_plans.history_status` | `not_asked` \| `unknown` \| `never` |
| `maintenance_plans.notes` | observação do dono sobre este item neste carro |
| `maintenance_plans.origin` | **ampliada** para `suggested \| user \| manufacturer \| manual \| admin \| external_provider` |
| `maintenance_items.default_strategy` | a estratégia do item como conceito |
| `maintenance_items.powertrain_requirement` | `any` \| `combustion` \| `spark_ignition` \| `high_voltage` |
| `maintenance_items.history_question` / `history_priority` | a pergunta em pt-BR e o quanto ela importa |
| `vehicle_profile_answers` | o que o dono contou sobre como o carro é feito |

**Três estados de aplicabilidade, e o terceiro é o que importa** (`maintenance/powertrain.go`):

```
o veículo tem       → plano normal, com os padrões do catálogo
o veículo não tem   → plano com strategy = 'not_applicable'; some de toda tela, e é desfazível
não dá para saber   → NENHUMA LINHA. Uma linha seria uma afirmação, e não temos uma.
```

A **única** derivação automática é o que uma motorização é: `vehicles.fuel_type` →
`Powertrain`. Nada aqui sabe marca, modelo ou intervalo de fabricante, e não pode crescer
para saber — isso seria o banco universal de manutenção automotiva que este projeto
deliberadamente não está construindo.

**Correia dentada × corrente de comando não é derivável.** Trocar `correia_dentada` por
`corrente_comando` no seed seria só trocar uma regra rígida por outra. Então nenhum dos dois
é sugerido: o dono é **perguntado uma vez** (`vehicle_profile_answers`, pergunta
`timing_drive`), e **"não sei" é resposta válida** — gravada, para a pergunta parar de
voltar, e sem criar plano de nenhum dos lados.

**`history_status` existe por um motivo só:** "não sei" não é "nunca foi feito", e nenhum dos
dois é um `maintenance_record`. Um registro afirma data e quilometragem, e quem não lembra
não tem nenhuma das duas — gravá-lo mesmo assim colocaria um fato fabricado no histórico
cujo valor inteiro é ser confiável (RN-03).

**Corrigir o `fuel_type` corrige os planos**, nas duas direções, com três travas:
`origin <> 'user'` (decisão do dono é dele), sem histórico (nunca contradizer um fato
registrado) e intervalos preservados (desfazer é um toque).

> Gatilho para uma tabela de perfil de verdade: quando existir **dado técnico por
> modelo** de fonte confiável, ou quando a mesma pergunta precisar de resposta diferente
> por eixo/posição. Não antes.

### RN-10 — Histórico do veículo × dados privados do dono

Classificação **documentada agora, implementada quando a transferência chegar**:

| Escopo veículo (viaja na venda) | Escopo dono (nunca viaja) |
|---|---|
| `odometer_readings` | valores e custos de tudo |
| `maintenance_records` (data, km, o que foi feito) | `expenses`, `abastecimentos` |
| `maintenance_record_items` (peças, garantia) | `seguros` (apólice, corretor, telefones) |
| nome da oficina *(a decidir)* | documentos, anexos, notas pessoais |

**O que custa fazer agora, e é tudo o que precisa:**
`recorded_by_user_id` nas tabelas de histórico + `vehicles` sem `user_id`. Só isso.

> Ponto aberto para o produto: custo de manutenção e histórico de abastecimento são
> exatamente o que dá poder ao comprador na negociação — e o que o vendedor menos quer
> entregar. Sugestão: transferência com **seleção explícita** do que vai junto, nunca
> automática.

---

## 3. Entidades

Convenção de nome: entidades genéricas em **inglês**; termos do domínio brasileiro em
**português** (`seguros`, `ipva`, `licenciamento`, `abastecimentos`, `multas`), seguindo
o `CLAUDE.md` do app.

### Identidade

- **`users`** — `id`, `email` (citext, único), `password_hash`, `name`, timestamps.
- **`refresh_tokens`** — token opaco rotativo. Guarda `token_hash` (sha256), nunca o token.
  Campos: `user_id`, `expires_at`, `revoked_at`, `replaced_by`. Detecção de reuso.
- **`password_reset_tokens`** — `token_hash`, `expires_at`, `used_at`.

### Veículo

- **`vehicles`** — `vehicle_type` (a coluna existe desde a 1ª migration; **o MVP-1 só
  aceita `car`**), `brand`, `model`, `version`,
  `manufacture_year`, `model_year`, `plate`, `renavam`, **`chassis`**, `fuel_type`,
  `color`, `nickname`, `fipe_code` (nullable, futuro),
  `current_mileage_km` + `current_mileage_at` (**cache**, ver RN-01).
- **`vehicle_ownerships`** — `vehicle_id`, `user_id`, `role`, `started_on`, `ended_on`.
  Índice único parcial: um veículo tem no máximo um `owner` ativo.
- **`odometer_readings`** — `vehicle_id`, `mileage_km`, `occurred_on`, `source`
  (`manual` | `maintenance` | `abastecimento` | `correction`),
  `source_maintenance_id` (FK nullable, `ON DELETE CASCADE`), `recorded_by_user_id`.

### Catálogo de veículos (D-16)

Espelho local da tabela FIPE, preenchido sob demanda. Todas as tabelas são **descartáveis**:
apagar tudo custa só as próximas requisições externas.

- **`vehicle_brands`** — `provider` (`fipe_parallelum`), `vehicle_type`, `external_id`,
  `name`, `synced_at`, `models_synced_at` (NULL = lista de modelos nunca buscada).
  Único por `(provider, vehicle_type, external_id)`. O `provider` fica **só aqui**: modelo
  e ano só são alcançáveis pela marca, então a FK já carrega a procedência.
- **`vehicle_models`** — `brand_id`, `external_id`, `name` (modelo **e** versão num campo
  só — a fonte não separa), `synced_at`, `years_synced_at`.
  Único por `(brand_id, external_id)`.
- **`vehicle_model_years`** — `model_id`, `external_id` (`"2017-6"`), `name`
  (`"2017 Híbrido"`), `year` (NULL no pseudo-ano 32000 de zero-quilômetro), `fuel_label`
  (palavra da fonte), `fuel_type` (**já no vocabulário de `vehicles.fuel_type`**),
  `fipe_code`. Único por `(model_id, external_id)`.
- **`vehicle_catalog_syncs`** — PK `(provider, vehicle_type)` + `synced_at`. Existe porque
  a *lista de marcas* é a única coleção sem linha-pai onde pendurar o timestamp.
- **`vehicle_fipe_prices`** — `model_year_id`, `fipe_code`, `price_cents`,
  `reference_month` (date, dia 1), `collected_at`.
  Único por `(model_year_id, reference_month)`. **Separado do catálogo de propósito:** marca,
  modelo e ano são fatos; preço é uma medição com data. Uma coluna `price` no ano faria toda
  leitura sobrescrever o histórico — e o histórico é a parte interessante.

O `vehicles` ganha `catalog_brand_id`, `catalog_model_id`, `catalog_model_year_id`
(nullable, `ON DELETE SET NULL`). Os campos de texto do veículo continuam sendo um
**retrato** do que o dono confirmou, nunca um espelho do catálogo — ver D-16.

### Manutenção

- **`maintenance_items`** — catálogo. `slug`, `name` (pt-BR), `kind`
  (`maintenance` | `care`), `vehicle_type` (`car` | `all`; a coluna comporta outros tipos),
  `owner_user_id` (NULL = global; preenchido = item custom do usuário),
  `default_interval_km`, `default_interval_months`.
- **`maintenance_plans`** — a **regra** por veículo. `vehicle_id`, `maintenance_item_id`,
  `interval_km` (nullable), `interval_months` (nullable), `alert_km`, `alert_days`,
  `origin` (`suggested` | `user`), `is_active`.
  Único por `(vehicle_id, maintenance_item_id)`.
- **`maintenance_records`** — o **fato**. `vehicle_id`, `occurred_on`, `mileage_km`
  (nullable quando todas as linhas são `care`),
  `kind` (`performed` | `declared`), `workshop_name`, `total_cost_cents`, `notes`,
  `recorded_by_user_id`, `deleted_at`.
- **`maintenance_record_items`** — as **linhas**. `maintenance_record_id`,
  `maintenance_item_id`, `description`, `part_brand`, `cost_cents`,
  `warranty_months`, `warranty_km`.

### Obrigações e seguro

- **`vehicle_obligations`** — IPVA e licenciamento. `kind`, `reference_year`, `due_on`,
  `amount_cents`, `paid_on`, `paid_amount_cents`.
  Único por `(vehicle_id, kind, reference_year)`.
- **`seguros`** — `insurer_name`, `policy_number`, `starts_on`, `ends_on`,
  `premium_cents`, `emergency_phone` (0800), `broker_name`, `broker_phone`.

> **Por que IPVA e licenciamento compartilham tabela e seguro não:** IPVA e licenciamento
> têm forma **idêntica** (ano de referência, vencimento, valor, pago). Seguro é um
> **contrato com período** e sete campos próprios de contato. O Princípio 4 do PRODUCT.md
> ("objetos de primeira classe, não reminders genéricos") é satisfeito por `kind`
> explícito e endpoints dedicados — não por multiplicar tabelas de forma idêntica.

### MVP-2 (modeladas, não implementadas)

- **`abastecimentos`** — `occurred_on`, `mileage_km`, `liters`, `price_per_liter_cents`,
  `total_cost_cents`, `station_name`, `is_full_tank`, `fuel_type`.
- **`expenses`** — `occurred_on`, `category` (com o CHECK da RN-04), `amount_cents`.
- **`attachments`** — `owner_type`, `owner_id`, `storage_key`, `mime`, `size_bytes`.

---

## 4. Relacionamentos

```
users ──< vehicle_ownerships >── vehicles ──┬── odometer_readings
  │            (M:N no tempo)      │        │      source: manual | maintenance |
  │                                │        │              abastecimento | correction
  ├── refresh_tokens               │        │
  └── password_reset_tokens        │        ├── maintenance_plans ──> maintenance_items
                                   │        │      interval_km / interval_months        ┐
                                   │        │      (ambos NULL = sem periodicidade)     │
                                   │        │                                           │ mesmo
                                   │        ├── maintenance_records                     │ item
                                   │        │      data, km, oficina, custo total       │
                                   │        │        └──< maintenance_record_items ─────┘
                                   │        │               custo, garantia
                                   │        │
                                   │        ├── vehicle_obligations  [ipva, licenciamento]
                                   │        ├── seguros
                                   │        │
                                   │        ├── expenses          [MVP-2]
                                   │        └── abastecimentos    [MVP-2]
                                   │
                                   └── (histórico carrega recorded_by_user_id)

    VIEW vehicle_costs = UNION ALL das fontes de dinheiro (sem risco de dupla contagem)
    func maintenance.ComputeDue(plans, últimos records, km atual, hoje) → []DueStatus
```

### Agregados (raiz de consistência transacional)

| Agregado | Raiz | Contém | Invariante |
|---|---|---|---|
| **Vehicle** | `vehicles` | ownerships, odometer_readings | `current_mileage_km` sempre reflete a leitura mais recente |
| **MaintenanceRecord** | `maintenance_records` | record_items, a odometer_reading gerada | criado/alterado/apagado em **uma transação** |
| **MaintenancePlan** | `maintenance_plans` | — | referencia um item do catálogo ativo para o `vehicle_type` |
| **User** | `users` | tokens | — |

Escrita de manutenção = **uma transação**: `record` + `items` + (se houver km)
`odometer_reading` + recálculo do cache de km do veículo.

---

## 5. Arquitetura

**Monólito modular.** Um binário, um banco, um processo.

```
        Flutter app
             │  HTTPS, JSON, /v1
             ▼
   ┌─────────────────────────────────────────┐
   │  chi router                             │
   │   requestID → logger → recover →        │
   │   CORS → rate limit → auth JWT          │
   ├─────────────────────────────────────────┤
   │  handler    (HTTP, DTO, validação)      │
   │  service    (regra de negócio, tx)      │
   │  repository (sqlc gerado)               │
   ├─────────────────────────────────────────┤
   │  identity │ vehicle │ maintenance │     │
   │  obligation │ insight                   │
   └─────────────────────────────────────────┘
             │ pgx pool
             ▼
        PostgreSQL 16
```

### Regras de fronteira

- Módulos conversam por **interface de service**, nunca chamando o repository um do outro.
- **`insight`** (dashboard, alerts, timeline) é o único módulo que lê tabelas de outros —
  e só através de queries **read-only**. É um módulo de read model, explicitamente assim.
- **DTO nunca é struct do sqlc.** Ver decisão D-02.
- `internal/platform/*` não conhece nenhum módulo de domínio. A dependência é sempre
  módulo → platform.

### O que NÃO tem, deliberadamente

Microsserviços, Kubernetes, Kafka, Redis, filas, cache distribuído, service mesh,
event sourcing, CQRS, gRPC, GraphQL, ORM, container de DI, mocks de banco.

---

## 6. Estrutura inicial do projeto

```
meu-auto-backend/
├── cmd/
│   └── api/
│       └── main.go                 # boot: config → log → db → migrate → serve
├── internal/
│   ├── app/                        # monta o grafo de objetos e devolve o handler
│   ├── platform/                   # sem conhecimento de domínio
│   │   ├── config/config.go        # env → struct, valida no boot
│   │   ├── database/               # pgxpool + migrate com advisory lock
│   │   ├── httpx/                  # router, middlewares, render, apperr → HTTP
│   │   ├── apperr/                 # erros de domínio tipados
│   │   ├── logging/                # slog JSON + request_id no context
│   │   ├── auth/                   # JWT, argon2id, middleware, usuário no context
│   │   ├── mailer/                 # interface + impl Resend + impl log (dev)
│   │   └── validate/
│   ├── identity/                   # users, sessões, reset de senha
│   │   ├── handler.go  service.go  repository.go  dto.go
│   │   └── db/                     # sqlc gerado
│   ├── vehicle/                    # vehicles, ownerships, odometer
│   │   ├── handler.go  service.go  repository.go  dto.go
│   │   ├── authorize.go            # authorizeVehicleAccess — RN-07
│   │   └── db/
│   ├── maintenance/                # items, plans, records
│   │   ├── due.go                  # <-- FUNÇÃO PURA. O coração do produto.
│   │   ├── due_test.go             # <-- table-driven, sem banco, cobertura alta
│   │   ├── handler.go  service.go  repository.go  dto.go
│   │   └── db/
│   ├── obligation/                 # ipva, licenciamento, seguros
│   └── insight/                    # dashboard, alerts, timeline (read models)
├── db/
│   ├── migrations/                 # golang-migrate — NNNN_nome.up.sql / .down.sql
│   ├── queries/                    # .sql do sqlc, um arquivo por módulo
│   └── seed/
│       └── maintenance_items.sql   # catálogo global: carro
├── api/
│   └── openapi.yaml                # <-- CONTRATO. Escrito à mão. Ver D-03.
├── test/
│   ├── testdb/                     # um banco migrado por teste, via testcontainers
│   ├── integration/                # a suíte: HTTP real, banco real, app.New
│   └── golden/                     # snapshots de resposta JSON (teste de compat)
├── docker-compose.yml              # SÓ Postgres. Nada mais.
├── Dockerfile                      # multi-stage, distroless
├── Makefile                        # run, test, migrate, sqlc, lint
├── sqlc.yaml
├── .env.example
├── CLAUDE.md
└── SPEC.md                         # este arquivo
```

---

## 7. Endpoints da primeira versão

Todos sob **`/v1`**. Autenticados com `Authorization: Bearer <access_token>`,
exceto os de auth e health.

### Auth e conta

```
POST   /v1/auth/register
POST   /v1/auth/login
POST   /v1/auth/refresh
POST   /v1/auth/logout
POST   /v1/auth/password-reset/request
POST   /v1/auth/password-reset/confirm
GET    /v1/me
PATCH  /v1/me
DELETE /v1/me                                  # LGPD — apaga tudo
```

### Veículos

```
GET    /v1/vehicles
POST   /v1/vehicles                            # cria + materializa planos sugeridos (RN-09)
GET    /v1/vehicles/{vehicleId}
PATCH  /v1/vehicles/{vehicleId}
DELETE /v1/vehicles/{vehicleId}
```

### Telas compostas (read models)

```
GET    /v1/vehicles/{vehicleId}/dashboard      # a tela principal, UMA request
GET    /v1/vehicles/{vehicleId}/alerts         # tudo que vence — 100% derivado
GET    /v1/vehicles/{vehicleId}/timeline       # histórico unificado, paginado por cursor
```

### Odômetro

```
GET    /v1/vehicles/{vehicleId}/odometer       # paginado
POST   /v1/vehicles/{vehicleId}/odometer       # 422 odometer_rollback se retroceder
DELETE /v1/odometer/{readingId}
```

### Catálogo de veículos

```
GET    /v1/vehicle-brands?vehicle_type=car
GET    /v1/vehicle-brands/{brandId}/models
GET    /v1/vehicle-models/{modelId}/years
GET    /v1/vehicle-model-years/{modelYearId}     # detalhe + valor FIPE
```

Selects progressivos: marca → modelo → ano → detalhe → `POST /v1/vehicles`.
Autenticados, mas **caller-scoped**: são dados de referência que toda conta pode ler. O
token está ali por causa do custo — cada miss gasta parte de uma cota diária compartilhada.

### Catálogo de manutenção e planos

```
GET    /v1/maintenance-items?vehicle_type=car&kind=maintenance
POST   /v1/maintenance-items                   # item custom do usuário

GET    /v1/vehicles/{vehicleId}/maintenance-plans
POST   /v1/vehicles/{vehicleId}/maintenance-plans
PATCH  /v1/maintenance-plans/{planId}
DELETE /v1/maintenance-plans/{planId}
```

### Manutenção realizada

```
GET    /v1/vehicles/{vehicleId}/maintenance-records
POST   /v1/vehicles/{vehicleId}/maintenance-records    # items[] no body, 1 transação
GET    /v1/maintenance-records/{recordId}
PATCH  /v1/maintenance-records/{recordId}
DELETE /v1/maintenance-records/{recordId}
```

### Obrigações e seguro

```
GET    /v1/vehicles/{vehicleId}/obligations?kind=ipva
POST   /v1/vehicles/{vehicleId}/obligations
PATCH  /v1/obligations/{obligationId}
DELETE /v1/obligations/{obligationId}

GET    /v1/vehicles/{vehicleId}/seguros
POST   /v1/vehicles/{vehicleId}/seguros
PATCH  /v1/seguros/{seguroId}
DELETE /v1/seguros/{seguroId}
```

### Operação

```
GET    /healthz                                # processo vivo
GET    /readyz                                 # ping no banco
```

### Convenções

- **Coleção aninhada no veículo** (`/vehicles/{id}/x`) para criar e listar;
  **recurso plano por id** (`/x/{id}`) para ler, alterar e apagar.
- **Paginação por cursor**, nunca offset: `?limit=50&cursor=<opaco>`.
- **Id gerado pelo cliente** (UUIDv7) no corpo do `POST`. `ON CONFLICT (id) DO NOTHING`
  → retry vira idempotente sem tabela de idempotency key.
- **Dinheiro em centavos**, inteiro: `total_cost_cents`. Nunca float, nunca string.
- **Datas civis em `YYYY-MM-DD`** (`occurred_on`, `due_on`). `timestamptz` só em
  `created_at` / `updated_at`.

### Formato de erro (contrato estável)

```json
{
  "error": {
    "code": "odometer_rollback",
    "message": "A quilometragem informada é menor que a última registrada.",
    "details": { "current_mileage_km": 98200, "current_mileage_at": "2026-08-10" }
  }
}
```

Códigos: `validation_failed`, `unauthorized`, `forbidden`, `not_found`,
`method_not_allowed`, `conflict`, `odometer_rollback`, `rate_limited`,
`upstream_unavailable`, `internal`.
**Códigos de erro são contrato — nunca renomear, nunca reaproveitar.**

Isso vale inclusive para 404 de rota inexistente e 405 de método errado: os handlers
padrão do `chi` respondem em `text/plain` e com corpo vazio, e foram substituídos. O app
faz parse de **todo** erro com o mesmo envelope — um path digitado errado não pode ser a
única resposta com formato diferente.

---

## 8. Decisões técnicas

### D-01 — `/v1` no path desde o primeiro endpoint

O `PRODUCT.md` estabelece: *"a shipped mobile app cannot be force-updated — API
compatibility across versions is a real constraint."* Esta é a restrição não-funcional
mais importante do backend.

**Regras dentro de uma versão — só mudanças aditivas:**

- Nunca remover campo. Nunca renomear campo. Nunca mudar tipo.
- Nunca apertar validação de campo existente.
- Campo novo sempre opcional, com default no servidor.
- **Valor novo em enum quebra app antigo.** O app trata desconhecido como default;
  o backend nunca renomeia nem reaproveita um valor de enum.

**Nulidade em campo existente.** Tornar `mileage_km` nulável na resposta de um
`maintenance_record` é uma mudança de contrato (um `as int` no cliente lança). Foi
feita mesmo assim, documentada na RN-03, porque o nulo só é produzido depois que o
cliente que omite km estiver publicado. Até lá, o app instalado continua mandando km
e nunca recebe nulo. Campo novo `care` na timeline é aditivo; `kind` permanece
`manutencao`.

### D-02 — DTO explícito por versão, nunca struct do sqlc no JSON

Se o DTO da API for o struct gerado do banco, toda migration vira potencialmente um
breaking change silencioso do contrato. Mapeamento manual em `dto.go` por módulo.
É repetitivo e é a decisão certa dado D-01.

### D-03 — Contrato app↔backend: OpenAPI escrito à mão

Resolve a decisão que o `CLAUDE.md` do app deixou explicitamente em aberto.

`api/openapi.yaml` versionado no repo do backend é a **fonte única do contrato**.
Os modelos Dart no app são escritos à mão, sem `build_runner`, sem `json_serializable`,
sem `openapi-generator`. O contrato exige que enum desconhecido caia em default seguro
em vez de lançar — um servidor que passe a devolver `fuel_type: "hidrogenio"` não pode
quebrar um app publicado. Em `json_serializable` isso é opt-in por enum (`unknownEnumValue`),
fácil de esquecer.

A compensação é `test/contract/openapi_paths_test.dart` no repositório do app, que lê
este `openapi.yaml` e falha se o app referenciar rota inexistente. Deste lado,
`TestRouterAndOpenAPIAgree` falha se o router e o spec divergirem.

**Por que escrito à mão e não gerado do código Go:** o spec é o contrato e o código o
implementa. Se o spec for gerado do código, qualquer refatoração vira mudança de
contrato sem ninguém perceber.

**Estado:** escrito e válido (`redocly lint`). Todo endpoint tem `operationId`.

**OpenAPI 3.0.3, não 3.1**, porque `nullable: true` do 3.0 é o que o contrato já usa, e o
app trata enum desconhecido como default seguro — um gerador que falha nesse valor
quebraria versões publicadas.

⚠️ **Falta o guarda automático.** O spec foi conferido campo a campo contra respostas reais
da API uma vez, na Fase 5 — todos os schemas batem. Mas nada impede a divergência amanhã.
O teste de snapshot (`test/golden/`) que trava isso ainda não existe; até ele existir, quem
mexer num DTO precisa mexer no spec na mesma alteração.

### D-04 — Stack

| Camada | Escolha | Nota |
|---|---|---|
| Router | `chi` | stdlib-compatível, middlewares componíveis |
| Driver | `pgx/v5` + `pgxpool` | direto, sem `database/sql` |
| Queries | `sqlc` | um pacote `db` gerado **por módulo** (múltiplos blocos `sql:`) |
| Migrations | `golang-migrate` | embutidas no binário via `embed.FS` |
| Log | `log/slog` (stdlib) | JSON no stdout |
| Senha | `argon2id` | não bcrypt |
| Token | JWT HS256 (acesso, 15min) + opaco rotativo (refresh, 30d) | |
| Teste | stdlib + `testify` + `testcontainers-go` | |
| Container | Docker multi-stage → distroless | |

### D-14 — O read model compõe, não recalcula

`internal/insight` é o **único** módulo que depende dos outros. A dependência é estritamente
de mão única (insight → vehicle, maintenance, obligation; nada importa insight) e somente
de leitura.

**A regra que importa:** ele chama os services dos donos e nunca reimplementa a derivação.
Recalcular "vencido" aqui criaria duas definições que podem divergir, e a tela passaria a
discordar do domínio atrás dela. O `Alert` unificado é uma **projeção**, não uma entidade
genérica de lembrete: nada é armazenado nesse formato, cada domínio mantém sua tabela e sua
regra, e o tipo existe só para uma tela mostrar correia, IPVA e garantia na mesma lista.

Duas queries próprias, ambas read-only, porque nenhum service consegue entregá-las:
a **timeline** (UNION com paginação por keyset sobre o conjunto combinado) e a **soma de
custos**.

**`sem_baseline` não é alerta.** É prompt de configuração, não prazo — listar 17 deles no
dia em que o veículo é criado enterraria a única coisa realmente vencida. O dashboard conta
separadamente.

**O dashboard não diz "custo total".** `tracked_categories` nomeia exatamente o que está
somado (manutenção, IPVA, licenciamento, seguro), porque combustível e despesas não existem
no MVP-1 e um campo chamado `total` seria lido como custo real e estaria errado pela maior
parte dele.

### D-13 — Aritmética de data civil num só lugar

`internal/platform/civil` concentra `Today`, `Parse`, `Format`, `FormatPtr`, `DaysBetween`,
`AddMonths` e `DaysInMonth`. Criado na Fase 4, quando o terceiro módulo ia duplicar os
mesmos helpers.

O que motivou: `AddMonths` tem uma sutileza que **não pode** divergir entre cópias — o
`AddDate` do Go normaliza 31/jan + 1 mês para 03/mar, o que faz garantia e aniversário de
manutenção derivarem a cada período. Uma definição, com os testes dela.

Data civil = `time.Time` à meia-noite UTC, que é o valor que o driver leva e traz de uma
coluna `date` sem alteração. Modelar essas datas como instantes é de onde vem bug de fuso.

### D-11 — Um trigger, e só um, para o cache de quilometragem

`vehicles.current_mileage_km` é mantido por um trigger em `odometer_readings`
(migration 000006), não por chamadas explícitas em cada módulo.

**Por quê, num projeto que mantém lógica fora do banco:** leituras são escritas por mais de
um módulo — `vehicle` hoje, `maintenance` agora, `abastecimento` depois — e cada um teria
que lembrar de chamar o mesmo recálculo, na mesma transação, com a mesma ordenação. Quem
esquecesse não falharia: serviria silenciosamente uma quilometragem velha para o dashboard
e para todo cálculo de manutenção. É a pior classe de bug possível neste schema.

**O custo é real e aceito:** isso é invisível do Go. Em troca, o invariante não pode ser
violado por nenhum escritor, presente ou futuro. Regra de negócio continua em Go; isto é
consequência estrutural de denormalizar uma coluna, que é onde trigger cabe.

### D-12 — `odometer_readings` é co-propriedade, deliberadamente

O módulo `vehicle` é dono das leituras manuais e de **toda leitura** do log. Qualquer módulo
pode **inserir** uma leitura marcada com sua própria `source`, dentro da própria transação —
a alternativa (chamar o service do vehicle) colocaria a leitura fora da transação do evento
e quebraria a atomicidade que a RN-01 exige.

A regra de monotonicidade **não** é duplicada: `vehicle.Service.CheckOdometerConsistency` é
exportado e o `maintenance` o usa através de uma porta. Uma definição da regra, um
mecanismo de cache, vários escritores.

### D-05 — Migrations no boot com advisory lock

Migrations embarcadas no binário (`db/embed.go`) e aplicadas no boot. Funciona no Railway
(que não tem release phase) e no VPS, e é seguro se um dia houver mais de uma instância:
o driver postgres do `golang-migrate` **já toma `pg_advisory_lock`** durante a execução,
então não reimplementamos o lock — se duas instâncias sobem juntas, uma aplica e a outra
espera e não encontra nada a fazer.

Schema sujo (migration que falhou no meio) **interrompe o boot** com instrução explícita,
em vez de aplicar migrations sobre um estado desconhecido.
Downs escritas, nunca executadas em produção.

### D-06 — Configuração 12-factor (Railway agora, VPS depois)

Struct única, parseada e **validada no boot** — o processo não sobe com config faltando.
`DATABASE_URL`, `PORT`, `JWT_SECRET`, `APP_ENV`, `LOG_LEVEL`, `RESEND_API_KEY`,
`CORS_ORIGINS`.

Railway injeta `PORT` e `DATABASE_URL` nativamente. **Nada específico do Railway entra
no código** — a migração para VPS é trocar variáveis e subir o `docker-compose`.

### D-07 — Erros

Pacote `apperr` com erro tipado (`Code`, `Message`, `Fields`, `cause`). Service devolve
`apperr`; um único middleware traduz para HTTP. `internal` nunca vaza mensagem interna
ao cliente — vai para o log com o `request_id`, e o cliente recebe o id para suporte.

### D-08 — Logging

`slog` em JSON no stdout. Middleware gera `request_id` (UUID) e injeta no context; toda
linha o carrega. **Nunca logar senha, token, hash, e-mail completo ou CPF.**
Sem APM, sem Prometheus, sem tracing no MVP.

### D-09 — Testes

| Alvo | Como |
|---|---|
| `maintenance/due.go` | Table-driven, sem banco. **Cobertura alta obrigatória.** É o único lugar com regra de negócio de verdade. |
| Repositórios | Integração com `testcontainers-go` e Postgres real + migrations. `sqlc` garante tipos, **não** semântica. |
| Handlers | `httptest` + banco real |
| Contrato | Snapshot do JSON em `test/golden/`; o teste falha se um campo sumir |

**Sem mock de banco.** Mock de SQL testa o mock, não a query.

**Isolamento por clone, não por limpeza.** Um container por binário de teste; as migrations
rodam uma vez num banco *template* e cada teste faz `CREATE DATABASE ... TEMPLATE`. Além de
ser barato (o Postgres copia arquivos, não repete migration), evita a armadilha do
`TRUNCATE ... CASCADE`, que alcança *tabelas* e apagaria o catálogo global semeado pela
migration 000005 — que não roda de novo.

**As fixtures passam pela API, nunca por INSERT.** Uma fixture escrita em SQL constrói
estados que a API teria recusado, e um teste que parte de um estado impossível não prova
nada sobre o sistema que roda. SQL direto só aparece nas asserções que a API deliberadamente
não responde: o que sobrou em disco depois de um soft delete, se um agregado gravou metade
de si mesmo.

**Três testes são trava, não teste** — é deles que vem a garantia de que os outros
continuam valendo alguma coisa:

| Trava | O que quebra o build |
|---|---|
| `TestEveryProtectedRouteIsInTheMatrix` | Uma rota servida que não está na tabela de autorização nem na lista curta das públicas. **Endpoint novo exige alguém dizer como ele é autorizado** (RN-07). |
| `TestRouterAndOpenAPIAgree` | Divergência de path ou método entre o router e `api/openapi.yaml`. Compara a forma das rotas, **não** os schemas. |
| `TestGoldenResponses` | Um campo renomeado, sumido, com outro tipo ou que virou nullable. |

O golden guarda a **forma** da resposta — cada chave e o tipo de cada folha —, não os
valores. Um snapshot de valores precisaria ser regerado a cada execução, porque toda
resposta carrega id e timestamp novos, e arquivo golden sempre desatualizado é arquivo que
ninguém lê. Regerar com `make test-golden` e **ler o diff**: mudança ali é mudança no que o
app já instalado recebe (D-01).

### D-10 — Não-funcionais

| Requisito | Decisão MVP-1 |
|---|---|
| **Segurança** | argon2id (m=19 MiB, t=2, p=1 — OWASP); JWT HS256 15min com algoritmo fixado; refresh opaco rotativo com detecção de reuso; rate limit in-memory, sem Redis; validação de todo input; `sqlc` só emite prepared statements |
| **Rate limit** | **Duas dimensões com orçamentos diferentes.** Por e-mail: apertado (10 logins/15min, 5 resets/hora) — é o que protege uma conta. Por IP: folgado (60/15min, 20/hora) — operadora móvel brasileira usa CGNAT, e um orçamento por IP do tamanho do de e-mail derruba a operadora inteira quando meia dúzia de assinantes erra a senha. Tirar a dimensão de IP deixaria um botnet percorrer uma lista vazada uma tentativa por conta |
| **Proxy** | `TRUST_PROXY` liga a leitura do `X-Forwarded-For`. **`true` só com proxy na frente** (Railway, Caddy): sem proxy, qualquer um forja o header e escapa do limite. `false` **com** proxy é pior ainda — todos os usuários do mundo viram um único IP |
| **CORS** | Em produção o padrão é **lista vazia = nenhum cabeçalho `Access-Control-*`**. É a postura correta para uma API só-mobile: o app não manda `Origin` e não está sujeito a CORS, então listar um domínio de que ninguém navega seria barulho fingindo ser segurança. `*` continua recusado em produção. Em development o padrão é `*` |
| **Autorização** | RN-07. Toda query filtra por ownership. 404 em vez de 403 |
| **Idempotência** | Id do cliente + `ON CONFLICT DO NOTHING` → 200 com o recurso existente |
| **Consistência** | Agregado = transação. Manutenção grava record + items + reading + cache de km atomicamente |
| **Concorrência** | Cache de km **recalculado da tabela dentro da mesma transação**, nunca incrementado. Dispensa lock explícito |
| **Auditoria** | `created_at`/`updated_at` em tudo; `recorded_by_user_id` no histórico. **Soft delete em `vehicles`** (o histórico é o ativo do produto; um toque errado não pode destruir anos de registro), **hard delete em `odometer_readings`** (leitura digitada errado é ruído, não histórico, e deixá-la corromperia todo intervalo derivado dela). Sem tabela de audit log |
| **LGPD** | `DELETE /v1/me` exige a senha atual e faz hard delete em cascata. Como `vehicles` não tem `user_id`, a cascata do banco não alcança os veículos: cada módulo com dado do usuário registra um `identity.UserDataEraser`, e o identity depende da **interface**, nunca do módulo. Erasers rodam antes do delete do usuário — falha de eraser não perde nada e o retry completa. Quando a transferência chegar, vira anonimização com preservação do histórico do veículo — **e isso precisa constar no aviso de privacidade desde já** |
| **Observabilidade** | `/healthz`, `/readyz`, logs estruturados. Nada mais até existir uma pergunta real que eles não respondam |
| **Timezone** | `DATE` para toda data civil elimina bug de fuso. `America/Sao_Paulo` só entra no cálculo de "hoje", passado explicitamente à função pura |

### D-15 — Só rotação dispara detecção de reuso

`refresh_tokens.revoked_reason` diz **por que** um token foi revogado: `rotation`, `logout`,
`reuse` ou `password_reset`. `Refresh` só toca o alarme — encerrar todas as sessões da conta
— quando o token reapresentado é `rotation`.

O motivo é assimetria de sinal. Um token **rotacionado** reapresentado significa que o
cliente legítimo está com o sucessor e alguém está com este: não dá para dizer quem é
atacante e quem é vítima, e derrubar tudo é a resposta certa. Os outros três são
invalidações deliberadas, e reapresentar um deles não prova nada além de que um token morto
continua morto.

A distinção não existia: logout e rotação escreviam `revoked_at` e mais nada. O efeito
prático aparecia longe da causa — o app repete um logout que estourou o tempo numa conexão
ruim, o retry cai na detecção de reuso, e o dono é deslogado do tablet por ter saído do
celular. Numa operadora móvel brasileira isso não é caso raro; é terça-feira.

A migration 000008 preenche as linhas já revogadas com `rotation`, que é a leitura
conservadora: mantém o alarme ligado para tudo que já estava na tabela. Uma `CHECK` garante
que `revoked_at` e `revoked_reason` andem juntos, para que uma query que esqueça o motivo
falhe em vez de reintroduzir a ambiguidade em silêncio. A lista de valores está duplicada
entre a migration e `internal/identity` — mude as duas juntas.

Coberto por `TestRefreshRotatesAndDetectsReuse` (o alarme dispara) e
`TestReplayingALoggedOutTokenLeavesOtherSessionsAlone` (o ruído em que ele não pode disparar).

---

### D-16 — Catálogo FIPE espelhado no Postgres, preenchido sob demanda

O cadastro de veículo pedia marca, modelo, versão, ano e combustível como **texto livre**.
É a primeira tela que todo usuário vê, e digitar "Volkswagem" ali contamina tudo o que vem
depois. O catálogo troca isso por selects progressivos.

**O número que decide a arquitetura: a cota gratuita do fornecedor é 500 requisições por
dia**, compartilhada por todos os usuários. Um proxy direto gastaria isso em algumas dezenas
de cadastros. Então:

```
requisição → Postgres → achou?  → devolve
                      → não     → fornecedor → persiste → devolve
```

Medido contra a API real: uma descida completa (marcas → modelos → anos → detalhe) custa
**exatamente 4 requisições externas, uma vez**. Depois disso, 5 ms vindos do Postgres contra
413 ms do fornecedor — e nenhuma chamada externa, para nenhum usuário, nunca mais.

Nada é importado antecipadamente. São ~107 marcas e dezenas de milhares de anos-modelo;
importar tudo seria um dia de requisições para preparar respostas que ninguém pediu.

**Decisões que acompanham:**

| Decisão | Por quê |
|---|---|
| `id` nosso, `external_id` deles | O domínio não fica preso ao id da Parallelum. Trocar de fornecedor vira uma sincronização, não um rewrite de todo veículo que referenciava |
| `provider` só em `vehicle_brands` | Modelo e ano só são alcançáveis pela marca; repetir a coluna seria dado desnormalizado que nenhuma query usa |
| Preço em tabela própria, por mês de referência | Marca/modelo/ano são fatos; preço é medição. Uma coluna `price` no ano faria toda leitura sobrescrever o histórico |
| TTL **só** no preço (7 dias) | Um 2017 será 2017 para sempre; o preço é republicado mensalmente. Sem cron, sem worker: a expiração é verificada na leitura |
| Sem retry | A cota é dura. Retry dobra o custo justamente das falhas onde menos ajuda — quota consumida vira quota batida duas vezes. Um 4xx nunca deve ser repetido |
| Sem lock de nenhum tipo | UNIQUE + `ON CONFLICT` resolve a corrida. Um lock aqui teria de ser mantido através de uma chamada HTTP a terceiro, que é como fornecedor lento vira pool esgotado |
| `upstream_unavailable` (503), não `rate_limited` | `rate_limited` diz "**você** está indo rápido demais". A cota estourada é nossa e compartilhada; culpar quem tocou na tela seria mentira que o app repete |
| Detalhe degrada em vez de falhar | É a última tela antes do cadastro. Fornecedor fora do ar devolve `fipe_price: null` com o resto vindo do banco — travar um formulário por causa de um enfeite seria pior |
| `fuel_type` traduzido no backend | A API devolve `fuel_label` ("Híbrido", para exibir) **e** `fuel_type` ("hibrido", que `POST /v1/vehicles` aceita). O app não fica com tabela de tradução de vocabulário de fornecedor |
| Só `car` na fronteira HTTP | Schema, cliente e mapa de tipos já suportam moto e caminhão. O limite está no mesmo lugar que em `POST /v1/vehicles`: é escopo de produto. Ampliar é apagar uma guarda |

**O veículo guarda um retrato, não uma consulta.** `brand`, `model`, `model_year`,
`fuel_type` e `fipe_code` continuam sendo texto no `vehicles`, mesmo com o vínculo
presente. Quando a fonte renomear `PRIUS 1.8 16V 5p Aut. (Híbrido)` para algo mais
arrumado, o veículo **não muda**: um histórico de serviço que se reescreve sozinho porque
um fornecedor arrumou uma string vale menos na revenda, e esse histórico é o ativo do
produto. Os três ids respondem "de qual entrada do catálogo veio isto?"; o retrato responde
"o que essa pessoa cadastrou?", e só o segundo precisa se sustentar diante de um comprador.

**O app manda um id só.** `catalog_model_year_id`; a marca e o modelo são derivados no
servidor. Um trio inconsistente não é expressável, e um id inventado não vira FK.

---

## 9. Decisões adiadas

Registradas com o **gatilho** que as reabre. Nenhuma deve ser implementada "por via das dúvidas".

| Decisão | Gatilho para reabrir |
|---|---|
| Anexos / object storage | MVP-2, junto com despesas |
| Push notification | Quando o pull do dashboard não bastar. Vira cron chamando a mesma função pura |
| Denormalizar `next_due_km/date` | Quando o push precisar varrer todos os veículos em batch |
| Ledger central de custos | Parcelamento, rateio entre pessoas, ou NF unificada |
| `vehicle_components` | Posição de pneu, troca parcial ou rodízio |
| Transferência de histórico | Produto validado. Exige verificação de propriedade e seleção explícita do que vai junto |
| Unicidade de chassi/placa | Junto com a transferência, e só com verificação (RN-08) |
| **Moto e outros tipos de veículo** | Carro validado. A coluna `vehicle_type` já existe desde a 1ª migration: vira seed de catálogo, **não** migration com backfill |
| Login social (Google/Apple) | Aditivo: tabela `user_identities`. Não quebra e-mail+senha |
| **Troca de e-mail da conta** | `PATCH /v1/me` só altera o nome. O e-mail é o canal de recuperação da conta: trocá-lo exige verificação no endereço novo e aviso ao antigo, e meio fluxo desses é pior que nenhum |
| **Verificação de e-mail no cadastro** | Hoje a conta nasce ativa. Entra quando houver algo que dependa de e-mail confiável (transferência de histórico, cobrança) |
| **Limpeza de tokens expirados** | A query `DeleteExpiredRefreshTokens` existe e nada a chama. Vira um cron quando a tabela crescer o bastante para importar |
| **Limpar campo opcional do veículo** | `PATCH /v1/vehicles/{id}` usa `COALESCE`: campo ausente fica como está, e por isso não há como voltar um opcional para NULL. Precisa de um gesto explícito, não de sobrecarregar `null` — que depois de decodificado é indistinguível de "ausente" |
| **Índices em `plate` e `chassis`** | Não existem: hoje nada busca por eles. Entram junto com a transferência, via `CREATE INDEX CONCURRENTLY` |
| **Catálogo por marca/modelo** | Os intervalos semeados são genéricos de mercado, não de fabricante — um Corolla e um Uno não compartilham intervalo de correia. Se virar reclamação, o caminho é catálogo por marca/modelo. É problema de **conteúdo**, não de código |
| **Editar as linhas de um registro** | `PATCH /maintenance-records/{id}` altera data, km, oficina, valor e observação — **não** os itens. Trocar linha a linha exige decidir o que acontece com o relógio dos itens removidos |
| **Atualizar os planos sugeridos** | O flag `origin` já separa `suggested` de `user`, e qualquer edição promove para `user`. Falta o job que atualiza os `suggested` sem tocar nos customizados |
| Parcelamento de IPVA | Se o uso mostrar que importa |
| Amortização do prêmio de seguro no custo mensal | Quando o relatório de custo mensal existir |
| Multas | Categoria de `expenses` no MVP-2; entidade própria só se houver integração |
| ~~FIPE~~ | **FEITO** — ver D-16. Antecipado porque remove quatro campos de texto livre da primeira tela do produto, não pelo valor de mercado |
| **Histórico mensal de valor FIPE** | A tabela `vehicle_fipe_prices` já guarda por mês de referência e nada além da linha mais recente é lido. Reabre quando houver tela de desvalorização — aí é um `ORDER BY`, e um job mensal para adensar os meses que ninguém consultou |
| **Atualizar o catálogo já sincronizado** | `synced_at`, `models_synced_at` e `years_synced_at` existem e nada os expira. Reabre quando um modelo novo demorar a aparecer: vira um `WHERE synced_at < X` na mesma leitura, sem cron |
| **Moto e caminhão no catálogo** | Schema, cliente e mapa de tipos já suportam. A guarda está em `normalizeVehicleType`, junto com a de `POST /v1/vehicles` — as duas caem juntas |
| SENATRAN / DETRAN | Não é pré-requisito de nada. Última fila |
| Offline / sincronização | Fora de escopo por decisão de produto. Id do cliente já deixa a porta aberta |
| Redis, filas, i18n | Sem gatilho previsto |

---

## 10. Roadmap sugerido

### Fase 0 — Fundação (antes de qualquer feature)

`cmd/api` sobe, `/healthz` responde, `docker-compose` com Postgres, primeira migration,
`sqlc` gerando, `apperr` + `httpx` + `logging` + `config` prontos, CI rodando teste.
Sem isso, tudo depois vira retrabalho.

### Fase 1 — Identidade

Register, login, refresh, logout, reset de senha, `/v1/me`, `DELETE /v1/me`.

### Fase 2 — Veículo e odômetro

CRUD de veículo com ownership, `authorizeVehicleAccess`, odômetro com RN-01 e a regra
de rollback. **Aqui o app já tem tela de verdade.**

### Fase 3 — Manutenção

Catálogo + seed (somente carro), planos com materialização automática (RN-09),
registros com linhas em transação, `due.go` com sua suíte de testes.
**Esta é a fase de maior valor e maior risco. Reserve o dobro do tempo.**

### Fase 4 — Prazos

IPVA, licenciamento, seguro. CRUD puro, sem regra. Rápida.

### Fase 5 — Read models

`/dashboard`, `/alerts`, `/timeline`. Fecha o MVP-1.

### Fase 6 — MVP-2

Abastecimento → despesas → view `vehicle_costs` → relatório de custo e custo/km →
anexos → alerta de garantia.

### Fase 7 — Em diante

Push · durabilidade de componente · exportação de histórico · FIPE · transferência.

---

## Riscos conhecidos

1. **Intervalos sugeridos genéricos estarão errados para carros específicos.** Mitigado
   por `origin = 'suggested'` e tudo editável. Se virar reclamação, o caminho é catálogo
   por marca/modelo — não adivinhação melhor.
2. **O catálogo de manutenção é a aposta de conteúdo do produto.** Um item faltando ou
   mal nomeado aparece para todos os usuários de uma vez. Vale revisar a seed com um
   mecânico antes do lançamento — isso é conteúdo, não código.
3. **Sem offline**, o app falha exatamente no posto e no estacionamento — os momentos
   descritos como principais no `PRODUCT.md`. Risco de produto, já registrado lá.
4. **Sem combustível, o MVP-1 não pode prometer "custo real do veículo"** — que é o pilar
   nº 2 do posicionamento. A comunicação do app precisa refletir isso.
