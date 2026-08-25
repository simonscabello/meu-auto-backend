# Deploy — Meu Auto backend

Railway por enquanto, VPS depois. Nada no código é específico do Railway: migrar é trocar
variáveis e subir o `docker-compose` (SPEC.md D-06).

---

## O que já foi verificado

A imagem de produção foi construída e executada localmente antes deste documento existir.
Confirmado na imagem real, não no `go run`:

| | |
|---|---|
| Imagem | **23,7 MB** (distroless, binário estático, `CGO_ENABLED=0`) |
| `DATABASE_URL` com esquema `postgresql://` | normalizado — é o formato que o Railway entrega |
| `PORT` dinâmico | respeitado (testado em 3000) |
| `America/Sao_Paulo` no distroless | carrega, via `time/tzdata` embutido — sem isso o boot aborta |
| Migrations no boot | aplicadas antes de escutar |
| CORS em produção | **nenhum cabeçalho** `Access-Control-*` emitido |
| `SIGTERM` | drena e sai com código 0 em ~2s |
| Config inválida | recusa subir e diz **todos** os problemas de uma vez |

---

## 1. Provisionar

No projeto do Railway:

1. **Add → Database → PostgreSQL.**
2. **Add → GitHub Repo →** `meu-auto-backend`.

O `railway.toml` já declara o Dockerfile, a sonda e a política de reinício. Não é preciso
configurar build.

## 2. Variáveis de ambiente

No serviço da API, aba **Variables**:

| Variável | Valor | Por quê |
|---|---|---|
| `APP_ENV` | `production` | Liga as validações estritas abaixo |
| `DATABASE_URL` | `${{Postgres.DATABASE_URL}}` | Referência ao serviço, não um valor copiado — sobrevive a uma recriação do banco |
| `JWT_SECRET` | *gerar, ver abaixo* | Mínimo 32 caracteres |
| `RESEND_API_KEY` | `re_...` | **Obrigatório.** Sem ele o reset de senha escreveria o link no log |
| `MAIL_FROM` | `Meu Auto <nao-responda@seu-dominio>` | Domínio precisa estar verificado no Resend |
| `TRUST_PROXY` | `true` | O Railway é um proxy. Sem isso, **todos os usuários do mundo compartilham um IP** no rate limit |
| `LOG_LEVEL` | `info` | |
| `PASSWORD_RESET_URL` | `meuauto://redefinir-senha` | Deep link do app. Ajuste quando o esquema do app for definido |

**Não defina `PORT`** — o Railway injeta.

**Não defina `CORS_ORIGINS`.** Em produção o padrão é vazio, que é o correto: o único
cliente é um app mobile, que não manda `Origin` e não está sujeito a CORS. Listar um
domínio de que ninguém navega seria barulho fingindo ser segurança.

Gerar o segredo:

```bash
openssl rand -base64 48
```

> Trocar `JWT_SECRET` invalida **todos** os tokens de acesso em circulação — todo mundo é
> deslogado. Faça isso deliberadamente, não como parte de um deploy de rotina.

## 3. Primeiro deploy

O Railway constrói e sobe sozinho ao conectar o repo. Acompanhe os logs: as migrations
rodam antes do servidor escutar, e o boot informa a versão do schema.

Se a config estiver errada, o processo **não sobe** e o log traz a lista completa de
problemas — não um por vez.

## 4. Verificar depois de subir

```bash
BASE=https://seu-servico.up.railway.app
curl -s $BASE/healthz && curl -s $BASE/readyz
```

`/readyz` só responde `200` depois de um ping bem-sucedido no banco. Um `503` com
`"reason":"database"` significa que a API subiu mas não alcança o Postgres — confira se
`DATABASE_URL` está como referência `${{Postgres.DATABASE_URL}}`.

Depois, um fluxo de ponta a ponta:

```bash
curl -s -X POST $BASE/v1/auth/register -H 'Content-Type: application/json' -d '{"name":"Teste","email":"voce@exemplo.com","password":"uma-senha-boa-123"}'
```

Deve responder `201` com um `access_token`. Apague essa conta depois com `DELETE /v1/me`.

## 5. Rollback

O Railway guarda os deploys anteriores: **Deployments → o anterior → Redeploy.**

⚠️ **Isso reverte o código, não o banco.** As migrations só têm caminho de ida em produção
(SPEC.md D-05). Se um deploy incluiu migration, voltar o código pode deixá-lo rodando
contra um schema à frente dele. Antes de reverter, confira se a versão anterior convive com
o schema atual — o que é verdade quando a migration foi puramente aditiva, e falso quando
ela removeu ou renomeou coluna.

Essa é uma razão prática para manter as migrations aditivas: elas tornam o rollback um
botão em vez de um incidente.

## 6. Migrar para VPS

Nada muda no código. No servidor:

1. `docker compose up -d` para o Postgres (ou um Postgres gerenciado).
2. Rodar a mesma imagem com as mesmas variáveis, mais `PORT`.
3. Caddy na frente para TLS — e `TRUST_PROXY=true` continua correto.

O que você assume ao sair do Railway: **backup do banco** e renovação de certificado. São as
duas coisas que ele fazia por você.

---

## Riscos conhecidos no ambiente hospedado

- **Rate limit é em memória.** Com mais de uma instância, o limite efetivo multiplica pelo
  número de instâncias. Por isso `numReplicas = 1` no `railway.toml`. Escalar horizontalmente
  exige mover o limitador para um armazenamento compartilhado primeiro (SPEC.md).
- **Tokens de refresh expirados nunca são limpos.** A query existe e nada a chama. A tabela
  cresce; vira um cron quando o tamanho importar.
- **Sem métricas nem tracing.** Só logs estruturados em JSON, que o Railway indexa. Isso é
  deliberado até existir uma pergunta que os logs não respondam.
