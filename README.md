# regru

Неофициальная консольная утилита для управления DNS-зонами на reg.ru через REG.API v2.
С reg.ru не связана и им не одобрена.

## Установка

Репозиторий приватный, поэтому `go install ...@latest` без дополнительных настроек Go proxy не сработает.
Собирайте из клона, нужен Go 1.26 или новее:

    git clone <адрес репозитория> regru-cli
    cd regru-cli
    make build

Получится `./regru`, скопируйте его в любой каталог из `PATH`.

`make cross` собирает бинарники для linux и darwin на amd64 и arm64 в `dist/`.

## Настройка

Конфиг лежит в `~/.config/regru/config.yaml` (учитывается `XDG_CONFIG_HOME`; переопределяется через `--config`
или `REGRU_CONFIG`). Пароли в нём не хранятся, только ссылки `env:`:

    version: 1
    default_account: main
    accounts:
      main:
        username: my-login
        password: env:REGRU_PASSWORD_MAIN
      other:
        username: env:REGRU_USERNAME_OTHER
        password: env:REGRU_PASSWORD_OTHER
        env_file: ~/.config/regru/secrets.env
    defaults:
      max_deletes: 10
      timeout: 5m
      request_timeout: 30s

`password` обязан быть ссылкой `env:VAR`; пароль, записанный как есть, делает весь конфиг невалидным (код выхода 2).
`username` может быть логином как есть или ссылкой `env:VAR`; пустой логин считается ошибкой. Переменные
ищутся сначала в окружении процесса, затем в `env_file`. Без конфига используются переменные окружения
`REGRU_USERNAME` и `REGRU_PASSWORD`.

Аккаунт выбирается через `--account NAME` или `REGRU_ACCOUNT`, иначе берётся `default_account`.
`--account auto` находит аккаунт, которому принадлежит домен из командной строки, перебирая домены
всех настроенных аккаунтов. Результат кэшируется на час в `~/.cache/regru/domains.json`
(`XDG_CACHE_HOME`); команды, меняющие зону, всегда ищут домен заново.

    regru account list
    regru account check
    regru --account auto dns get example.test

`account list` показывает настроенные аккаунты и ссылки на переменные, но никогда не секреты.
`account check` логинится и печатает логин, id пользователя и то, является ли аккаунт реселлером.

Запросы ограничены одним в 3 секунды. Журналы, кэши и блокировки хранятся в XDG-каталогах state и cache
с правами только для владельца.

## Использование

    regru version
    regru domain list
    regru domain get example.test
    regru dns get example.test
    regru dns get example.test --name www --type A

    regru dns export example.test --file zone.yaml
    regru dns export example.test --scope managed-zone --file zone.yaml
    regru dns plan --file zone.yaml --save plan.json
    regru dns plan --file zone.yaml --detailed-exitcode
    regru dns apply --plan plan.json
    regru dns apply --file zone.yaml --dry-run

    regru dns add example.test www A 192.0.2.10
    regru dns rm example.test www A 192.0.2.10
    regru dns rm example.test old A --all-values
    regru dns set example.test www A 192.0.2.10 192.0.2.11
    regru dns set example.test @ TXT --yes -- "-all"

Каждое изменение планируется, показывается, подтверждается (или `--yes`), сверяется с текущей зоной прямо
перед отправкой и проверяется повторным чтением зоны после. Число удалений ограничено `--max-deletes`,
а план, после которого в зоне не останется изменяемых записей, отклоняется без `--allow-empty-zone`.
NS-записи апекса никогда не меняются. Поэтому удаление или замена последней изменяемой записи зоны через
`dns rm` или `dns set` тоже требует `--allow-empty-zone`.

Имена записей указываются относительно зоны (`www`, `@`, `_sip._tcp`, `*`). Полное имя пишется с точкой
на конце (`www.example.test.`); относительное имя, оканчивающееся на имя зоны, отклоняется как неоднозначное.

`dns rm --all-values` удаляет все значения RRset в том виде, в каком они прочитаны прямо перед планированием.

У манифеста есть scope. `listed-rrsets` (по умолчанию для `dns export`) затрагивает только RRset'ы,
перечисленные в `rrsets`. `managed-zone` вдобавок удаляет изменяемые записи, которых нет в манифесте.
Записи, которыми утилита не может управлять точно (неподдерживаемые типы, дубли, RRset, где смешаны обычные
и такие записи), выгружаются в `readonly` и никогда не меняются.

Каждое применённое изменение пишется в журнал, по одной JSON-строке на шаг, в
`~/.local/state/regru/journal/<user_id>/<zone>/<time>-<run_id>.jsonl` (`XDG_STATE_HOME`). Для каждой зоны
хранятся 50 последних завершённых запусков; запуски, закончившиеся частично или с неизвестным результатом,
при ротации не удаляются.

`--output json` печатает в stdout ровно один JSON-документ:
`{"schema_version":1,"status":...,"data":...,"errors":[...],"warnings":[...]}`.

## Коды выхода

| код | значение |
|---|---|
| 0 | успех или нечего делать |
| 1 | ошибка, отказ API или нужно подтверждение |
| 2 | неверные аргументы или конфигурация |
| 3 | применено частично, результат неизвестен или проверка не сошлась |
| 4 | план устарел или несовместим с текущей зоной |

`dns plan --detailed-exitcode` возвращает 0, если изменений нет, 2, если они есть, и 1 при любой ошибке.

## Особенности API

Проверено вживую на обычном (не партнёрском) аккаунте:

- `update_records` доступен только партнёрам, поэтому изменения идут отдельными `add_*` и `remove_record`.
- У записей нет id. `remove_record` удаляет ровно одну запись по `subdomain`, `record_type`, `priority`
  и `content`; `content` берётся ровно как в `get_resource_records`, `priority` равен `prio` для MX и SRV
  и `"0"` для остальных типов.
- `content` у SRV имеет вид `<weight> <port> <target>`, приоритет лежит в `prio`; `add_srv` принимает
  полное имя сервиса в `service` вместо `subdomain`. `content` у CAA имеет вид `<flags> <tag> "<value>"`.
- У AAAA `prio` равен null. SOA приходит строками с суффиксами (`1d`, `3h`).
- TXT принимается только в ASCII, кириллица отвергается с `TEXT_CONTAINS_INVALID_CHARACTERS`.
- CNAME не уживается с другими записями на том же имени (`CONFLICT_CNAME`), поэтому смена значения
  идёт через удаление и добавление.
- Ошибки по домену (`CONFLICT_CNAME`, `TEXT_CONTAINS_INVALID_CHARACTERS`) приходят внутри ответа
  с общим `result: success`.
- Повторное добавление существующей A-записи ничего не меняет.
- Лимит 1200 запросов в час и белый список IP настраиваются в кабинете.

## Разработка

    make test lint cross

Live-тесты работают с настоящей зоной и в обычную сборку не входят. Их нужно включать явно:

    REGRU_LIVE_ZONE=<zone> REGRU_LIVE_ALLOW_WRITE=1 REGRU_USERNAME=<login> REGRU_PASSWORD=<password> \
      go test -tags live ./internal/regruzone/ -run Live -v

Они создают и удаляют записи только под случайным префиксом `regru-live-<id>` и в конце сравнивают зону
с исходным состоянием. Используйте зону, которую не жалко.
