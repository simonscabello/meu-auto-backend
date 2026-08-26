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

### Manutenção

- **`maintenance_items`** — catálogo. `slug`, `name` (pt-BR), `kind`
  (`maintenance` | `care`), `vehicle_type` (`car` | `all`; a coluna comporta outros tipos),
  `owner_user_id` (NULL = global; preenchido = item custom do usuário),
  `default_interval_km`, `default_interval_months`.
- **`maintenance_plans`** — a **regra** por veículo. `vehicle_id`, `maintenance_item_id`,
  `interval_km` (nullable), `interval_months` (nullable), `alert_km`, `alert_days`,
  `origin` (`suggested` | `user`), `is_active`.
  Único por `(vehicle_id, maintenance_item_id)`.
- **`maintenance_records`** — o **fato**. `vehicle_id`, `occurred_on`, `mileage_km`,
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

Escrita de manutenção = **uma transação**: `record` + `items` + `odometer_reading` +
recálculo do cache de km do veículo.

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

### Catálogo e planos

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
`method_not_allowed`, `conflict`, `odometer_rollback`, `rate_limited`, `internal`.
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

### D-02 — DTO explícito por versão, nunca struct do sqlc no JSON

Se o DTO da API for o struct gerado do banco, toda migration vira potencialmente um
breaking change silencioso do contrato. Mapeamento manual em `dto.go` por módulo.
É repetitivo e é a decisão certa dado D-01.

### D-03 — Contrato app↔backend: OpenAPI escrito à mão

Resolve a decisão que o `CLAUDE.md` do app deixou explicitamente em aberto.

`api/openapi.yaml` versionado no repo do backend é a **fonte única do contrato**.
O app gera o client Dart a partir dele.

**Por que escrito à mão e não gerado do código Go:** o spec é o contrato e o código o
implementa. Se o spec for gerado do código, qualquer refatoração vira mudança de
contrato sem ninguém perceber.

**Estado:** escrito e válido (`redocly lint`), cobrindo os 41 endpoints. Todo endpoint tem
`operationId` — é dele que sai o nome do método no client Dart; sem ele o gerador deriva do
path e o nome muda junto com a rota.

**OpenAPI 3.0.3, não 3.1**, por causa do consumidor: o `openapi-generator` lida bem com
`nullable: true` do 3.0 e mal com `type: [string, "null"]` do 3.1.

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
| FIPE | Depois do MVP-2. Traz valor de mercado e engajamento |
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
