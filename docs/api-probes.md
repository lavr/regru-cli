# REG.API v2: живые проверки DNS-зоны

Дата: 2026-10-18. Тестовая зона на ns1/ns2.reg.ru, обычный (не партнёрский) аккаунт.
Три запуска (`r1018a`: 144 запроса, `r1018b`: 42, `r1018c`: 12, повтор P10 с селектором AAAA `priority: "0"`; пауза 2 с).
Все тестовые записи под отдельным префиксом, каждая проверка убирала за собой; после обоих запусков
зона вне префикса и SOA совпали с исходными. Неопределённых исходов не было, запасная уборка не применялась.
Вычищенные ответы: `internal/regruapi/testdata/`. Полные цепочки запрос, readback, remove, readback: раздел
«Цепочки доказательств» ниже.

Методика: в один RRset добавляются два разных значения (цель и контроль), после каждой мутации
зона перечитывается, затем `remove_record` цели. `pass`: цель исчезла, контроль на месте, остальная зона
не изменилась. Результат проверки отделён от результата уборки.

## Точное удаление

| проверка | тип | чем отличаются цель и контроль | exact-delete | уборка |
|---|---|---|---|---|
| P1 | MX | только priority | pass | verified |
| P2 | MX | только host | pass | verified |
| P3 | SRV | только weight | pass | verified |
| P4 | SRV | только port | pass | verified |
| P5a | SRV | только priority | pass | verified |
| P5b | SRV | только target | pass | verified |
| P6a | CAA | только tag | pass | verified |
| P6b | CAA | только flags | pass | verified |
| P6c | CAA | только value | pass | verified |
| P7 | A | адрес (после попытки дубля) | pass | verified |
| P9-txt1 | TXT | пробелы (23 байта) | pass | verified |
| P9-txt2 | TXT | двойные кавычки (20 байт) | pass | verified |
| P9-txt3 | TXT | обратный слеш (22 байта) | pass | verified |
| P9-txt4 | TXT | `=` и `;` (25 байт) | pass | verified |
| P9-txt5 | TXT | 255 ASCII-байт | pass | verified |
| P9-txt6 | TXT | 256 ASCII-байт | pass | verified |
| P9-txt7 | TXT | кириллица (22 байта UTF-8) | not-tested: add отвергнут `TEXT_CONTAINS_INVALID_CHARACTERS` | verified |
| P10 | AAAA | адрес (r1018b и r1018c) | pass | verified |
| P11 | NS (поддомен) | target | pass | verified |

Во всех TXT-случаях с `pass` значение после чтения совпало с отправленным побайтно.

## Селектор `remove_record`, который фактически отправлялся

`content` всегда брался ровно из `get_resource_records`, `priority` из `prio` того же ответа (строкой).

| тип | `content` | `priority` в запросе |
|---|---|---|
| A | `192.0.2.1` | `"0"` |
| AAAA | `2001:db8::1` | `"0"` (запуск r1018c, pass). В r1018b уходил артефакт `"None"`, он тоже сработал, но не используется; вариант без `priority` не проверялся |
| CNAME | `t1.example.net.` | `"0"` |
| MX | `mx-a.example.net.` | priority записи |
| TXT | текст как есть | `"0"` |
| NS (поддомен) | `ns1.example.net.` | `"0"` |
| SRV | `<weight> <port> <target.>` | priority записи |
| CAA | `<flags> <tag> "<value>"` (с кавычками) | `"0"` |

## Как API отдаёт записи (`get_resource_records`)

| тип | `prio` | `content` |
|---|---|---|
| A | 0 | `192.0.2.1` |
| AAAA | **null** | `2001:db8::1` |
| CNAME | 0 | `t1.example.net.` |
| MX | priority | `mx-a.example.net.` |
| TXT | 0 | текст как есть, без кавычек и сегментации |
| NS поддомена (в этих проверках, `add_ns` без `record_number`) | 0 | `ns1.example.net.` |
| NS апекса | 0 и 1 | `ns1.reg.ru.`, `ns2.reg.ru.` |
| SRV | priority | `<weight> <port> <target.>`, плюс отдельные поля `weight`, `port` |
| CAA | 0 | `<flags> <tag> "<value>"` |

- `prio` приходит числом или null, `service_id` строкой.
- `soa` приходит строками с суффиксами: `{"ttl": "1d", "minimum_ttl": "3h"}`. В манифесте это секунды,
  нужна явная нормализация (`d`, `h`, `m`, `w` и число секунд по доке `update_soa`); `time.ParseDuration` суффикс `d`
  не понимает; неизвестный формат это ошибка, не 0.

## Поведение

- **Повтор add (P7).** Повторный `add_alias` того же A ответил `success` и второй записи не создал; кратность
  в этом RRset осталась 1. Для остальных типов повторный add не исследован.
- **CNAME (P8).** P8a: A, затем CNAME на том же имени: `CONFLICT_CNAME`, A на месте. P8b: CNAME, затем A:
  `CONFLICT_CNAME`, CNAME на месте. P8c: CNAME, затем второй CNAME: `CONFLICT_CNAME`, первый на месте.
  P8d: CNAME `t1`, `remove_record` (точный), add CNAME `t2`: работает.
- **Ошибки по домену.** `CONFLICT_CNAME` и `TEXT_CONTAINS_INVALID_CHARACTERS` приходят внутри общего
  `result: success`, в `answer.domains[].error_code`; у `CONFLICT_CNAME` в `error_params.conflicting_records`
  перечислены мешающие записи.
- **TXT.** Сервер принял и сохранил побайтно пробелы, двойные кавычки, обратный слеш, `=`, `;`, 255 и 256 ASCII-байт.
  Проверенный текст с кириллицей сервер отверг. Управляющие байты, NUL и длины больше 256 не исследованы.
- **SRV.** `add_srv` принимает полное имя сервиса в `service` (`_s1._tcp.<sub>`), отдельного `subdomain` нет.
- **NS поддомена.** `add_ns` без `record_number` работает.
- **Авторизация.** `username`/`password` отдельными полями формы при `input_format=json` работают.
- **Темп.** 186 запросов с паузой 2 с прошли без ошибок лимита. Это не доказывает, что 2 с достаточно
  для длительной работы: непрерывный поток с такой паузой это до 1800 запросов в час при лимите 1200.

## Не проверялось

- HTTPS (запись в зону не добавлялась).
- Публикация на NS reg.ru.
- Пустой TXT, управляющие символы, TXT длиннее 256 байт.
- `remove_record` для A и AAAA без `priority`.
- Повторный add для типов, кроме A.
- Поведение при превышении лимита запросов.
- service/get_list: фикстура синтетическая; в документации пагинации нет; полнота проверяется live-тестом сверкой с кабинетом.

## Цепочки доказательств (из журналов, вычищено)

Для каждой проверки: что отправлено, состояние имени до удаления, точный запрос `remove_record`, состояние после.
`readback` это записи имени по `get_resource_records` сразу после операции, в формате `[subname, rectype, prio, content]`.

### Запуск r1018a

#### P1

- `zone/add_mx` `{"subdomain": "mx1.probe", "priority": "10", "mail_server": "mx-a.example.net."}`: **success**
- readback: `[["mx1.probe", "MX", 10, "mx-a.example.net."]]`
- `zone/add_mx` `{"subdomain": "mx1.probe", "priority": "20", "mail_server": "mx-a.example.net."}`: **success**
- readback: `[["mx1.probe", "MX", 10, "mx-a.example.net."], ["mx1.probe", "MX", 20, "mx-a.example.net."]]`
- `zone/remove_record` `{"subdomain": "mx1.probe", "record_type": "MX", "priority": "10", "content": "mx-a.example.net."}`: **success**
- readback: `[["mx1.probe", "MX", 20, "mx-a.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "mx1.probe", "record_type": "MX", "priority": "20", "content": "mx-a.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P2

- `zone/add_mx` `{"subdomain": "mx2.probe", "priority": "10", "mail_server": "mx-a.example.net."}`: **success**
- readback: `[["mx2.probe", "MX", 10, "mx-a.example.net."]]`
- `zone/add_mx` `{"subdomain": "mx2.probe", "priority": "10", "mail_server": "mx-b.example.net."}`: **success**
- readback: `[["mx2.probe", "MX", 10, "mx-a.example.net."], ["mx2.probe", "MX", 10, "mx-b.example.net."]]`
- `zone/remove_record` `{"subdomain": "mx2.probe", "record_type": "MX", "priority": "10", "content": "mx-a.example.net."}`: **success**
- readback: `[["mx2.probe", "MX", 10, "mx-b.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "mx2.probe", "record_type": "MX", "priority": "10", "content": "mx-b.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P3

- `zone/add_srv` `{"service": "_s1._tcp.probe", "priority": "10", "weight": "5", "port": "5060", "target": "sip.example.net."}`: **success**
- readback: `[["_s1._tcp.probe", "SRV", 10, "5 5060 sip.example.net."]]`
- `zone/add_srv` `{"service": "_s1._tcp.probe", "priority": "10", "weight": "6", "port": "5060", "target": "sip.example.net."}`: **success**
- readback: `[["_s1._tcp.probe", "SRV", 10, "5 5060 sip.example.net."], ["_s1._tcp.probe", "SRV", 10, "6 5060 sip.example.net."]]`
- `zone/remove_record` `{"subdomain": "_s1._tcp.probe", "record_type": "SRV", "priority": "10", "content": "5 5060 sip.example.net."}`: **success**
- readback: `[["_s1._tcp.probe", "SRV", 10, "6 5060 sip.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "_s1._tcp.probe", "record_type": "SRV", "priority": "10", "content": "6 5060 sip.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P4

- `zone/add_srv` `{"service": "_s2._tcp.probe", "priority": "10", "weight": "5", "port": "5060", "target": "sip.example.net."}`: **success**
- readback: `[["_s2._tcp.probe", "SRV", 10, "5 5060 sip.example.net."]]`
- `zone/add_srv` `{"service": "_s2._tcp.probe", "priority": "10", "weight": "5", "port": "5061", "target": "sip.example.net."}`: **success**
- readback: `[["_s2._tcp.probe", "SRV", 10, "5 5060 sip.example.net."], ["_s2._tcp.probe", "SRV", 10, "5 5061 sip.example.net."]]`
- `zone/remove_record` `{"subdomain": "_s2._tcp.probe", "record_type": "SRV", "priority": "10", "content": "5 5060 sip.example.net."}`: **success**
- readback: `[["_s2._tcp.probe", "SRV", 10, "5 5061 sip.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "_s2._tcp.probe", "record_type": "SRV", "priority": "10", "content": "5 5061 sip.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P5a

- `zone/add_srv` `{"service": "_s3._tcp.probe", "priority": "10", "weight": "5", "port": "5060", "target": "a.example.net."}`: **success**
- readback: `[["_s3._tcp.probe", "SRV", 10, "5 5060 a.example.net."]]`
- `zone/add_srv` `{"service": "_s3._tcp.probe", "priority": "20", "weight": "5", "port": "5060", "target": "a.example.net."}`: **success**
- readback: `[["_s3._tcp.probe", "SRV", 10, "5 5060 a.example.net."], ["_s3._tcp.probe", "SRV", 20, "5 5060 a.example.net."]]`
- `zone/remove_record` `{"subdomain": "_s3._tcp.probe", "record_type": "SRV", "priority": "10", "content": "5 5060 a.example.net."}`: **success**
- readback: `[["_s3._tcp.probe", "SRV", 20, "5 5060 a.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "_s3._tcp.probe", "record_type": "SRV", "priority": "20", "content": "5 5060 a.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P5b

- `zone/add_srv` `{"service": "_s4._tcp.probe", "priority": "10", "weight": "5", "port": "5060", "target": "a.example.net."}`: **success**
- readback: `[["_s4._tcp.probe", "SRV", 10, "5 5060 a.example.net."]]`
- `zone/add_srv` `{"service": "_s4._tcp.probe", "priority": "10", "weight": "5", "port": "5060", "target": "b.example.net."}`: **success**
- readback: `[["_s4._tcp.probe", "SRV", 10, "5 5060 a.example.net."], ["_s4._tcp.probe", "SRV", 10, "5 5060 b.example.net."]]`
- `zone/remove_record` `{"subdomain": "_s4._tcp.probe", "record_type": "SRV", "priority": "10", "content": "5 5060 a.example.net."}`: **success**
- readback: `[["_s4._tcp.probe", "SRV", 10, "5 5060 b.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "_s4._tcp.probe", "record_type": "SRV", "priority": "10", "content": "5 5060 b.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P6a

- `zone/add_caa` `{"subdomain": "caa1.probe", "flags": 0, "tag": "issue", "value": "a.example.net"}`: **success**
- readback: `[["caa1.probe", "CAA", 0, "0 issue \"a.example.net\""]]`
- `zone/add_caa` `{"subdomain": "caa1.probe", "flags": 0, "tag": "issuewild", "value": "a.example.net"}`: **success**
- readback: `[["caa1.probe", "CAA", 0, "0 issue \"a.example.net\""], ["caa1.probe", "CAA", 0, "0 issuewild \"a.example.net\""]]`
- `zone/remove_record` `{"subdomain": "caa1.probe", "record_type": "CAA", "priority": "0", "content": "0 issue \"a.example.net\""}`: **success**
- readback: `[["caa1.probe", "CAA", 0, "0 issuewild \"a.example.net\""]]`
- `zone/remove_record` (уборка) `{"subdomain": "caa1.probe", "record_type": "CAA", "priority": "0", "content": "0 issuewild \"a.example.net\""}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P6b

- `zone/add_caa` `{"subdomain": "caa2.probe", "flags": 0, "tag": "issue", "value": "a.example.net"}`: **success**
- readback: `[["caa2.probe", "CAA", 0, "0 issue \"a.example.net\""]]`
- `zone/add_caa` `{"subdomain": "caa2.probe", "flags": 128, "tag": "issue", "value": "a.example.net"}`: **success**
- readback: `[["caa2.probe", "CAA", 0, "0 issue \"a.example.net\""], ["caa2.probe", "CAA", 0, "128 issue \"a.example.net\""]]`
- `zone/remove_record` `{"subdomain": "caa2.probe", "record_type": "CAA", "priority": "0", "content": "0 issue \"a.example.net\""}`: **success**
- readback: `[["caa2.probe", "CAA", 0, "128 issue \"a.example.net\""]]`
- `zone/remove_record` (уборка) `{"subdomain": "caa2.probe", "record_type": "CAA", "priority": "0", "content": "128 issue \"a.example.net\""}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P6c

- `zone/add_caa` `{"subdomain": "caa3.probe", "flags": 0, "tag": "issue", "value": "a.example.net"}`: **success**
- readback: `[["caa3.probe", "CAA", 0, "0 issue \"a.example.net\""]]`
- `zone/add_caa` `{"subdomain": "caa3.probe", "flags": 0, "tag": "issue", "value": "b.example.net"}`: **success**
- readback: `[["caa3.probe", "CAA", 0, "0 issue \"a.example.net\""], ["caa3.probe", "CAA", 0, "0 issue \"b.example.net\""]]`
- `zone/remove_record` `{"subdomain": "caa3.probe", "record_type": "CAA", "priority": "0", "content": "0 issue \"a.example.net\""}`: **success**
- readback: `[["caa3.probe", "CAA", 0, "0 issue \"b.example.net\""]]`
- `zone/remove_record` (уборка) `{"subdomain": "caa3.probe", "record_type": "CAA", "priority": "0", "content": "0 issue \"b.example.net\""}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P7

- `zone/add_alias` `{"subdomain": "dup.probe", "ipaddr": "192.0.2.1"}`: **success**
- readback: `[["dup.probe", "A", 0, "192.0.2.1"]]`
- `zone/add_alias` `{"subdomain": "dup.probe", "ipaddr": "192.0.2.2"}`: **success**
- readback: `[["dup.probe", "A", 0, "192.0.2.1"], ["dup.probe", "A", 0, "192.0.2.2"]]`
- `zone/add_alias` `{"subdomain": "dup.probe", "ipaddr": "192.0.2.1"}`: **success**
- readback: `[["dup.probe", "A", 0, "192.0.2.1"], ["dup.probe", "A", 0, "192.0.2.2"]]`
- `zone/remove_record` `{"subdomain": "dup.probe", "record_type": "A", "priority": "0", "content": "192.0.2.1"}`: **success**
- readback: `[["dup.probe", "A", 0, "192.0.2.2"]]`
- `zone/remove_record` (уборка) `{"subdomain": "dup.probe", "record_type": "A", "priority": "0", "content": "192.0.2.2"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P8a

- `zone/add_alias` `{"subdomain": "c1.probe", "ipaddr": "192.0.2.10"}`: **success**
- readback: `[["c1.probe", "A", 0, "192.0.2.10"]]`
- `zone/add_cname` `{"subdomain": "c1.probe", "canonical_name": "t1.example.net."}`: **domain error CONFLICT_CNAME**
- readback: `[["c1.probe", "A", 0, "192.0.2.10"]]`
- `zone/remove_record` (уборка) `{"subdomain": "c1.probe", "record_type": "A", "priority": "0", "content": "192.0.2.10"}`: **success**
- readback: `[]`

Итог: exact-delete=n/a (сценарий поведения), cleanup=verified

#### P8b

- `zone/add_cname` `{"subdomain": "c2.probe", "canonical_name": "t1.example.net."}`: **success**
- readback: `[["c2.probe", "CNAME", 0, "t1.example.net."]]`
- `zone/add_alias` `{"subdomain": "c2.probe", "ipaddr": "192.0.2.11"}`: **domain error CONFLICT_CNAME**
- readback: `[["c2.probe", "CNAME", 0, "t1.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "c2.probe", "record_type": "CNAME", "priority": "0", "content": "t1.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=n/a (сценарий поведения), cleanup=verified

#### P8c

- `zone/add_cname` `{"subdomain": "c3.probe", "canonical_name": "t1.example.net."}`: **success**
- readback: `[["c3.probe", "CNAME", 0, "t1.example.net."]]`
- `zone/add_cname` `{"subdomain": "c3.probe", "canonical_name": "t2.example.net."}`: **domain error CONFLICT_CNAME**
- readback: `[["c3.probe", "CNAME", 0, "t1.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "c3.probe", "record_type": "CNAME", "priority": "0", "content": "t1.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=n/a (сценарий поведения), cleanup=verified

#### P8d

- `zone/add_cname` `{"subdomain": "c4.probe", "canonical_name": "t1.example.net."}`: **success**
- readback: `[["c4.probe", "CNAME", 0, "t1.example.net."]]`
- `zone/remove_record` `{"subdomain": "c4.probe", "record_type": "CNAME", "priority": "0", "content": "t1.example.net."}`: **success**
- readback: `[]`
- `zone/add_cname` `{"subdomain": "c4.probe", "canonical_name": "t2.example.net."}`: **success**
- readback: `[["c4.probe", "CNAME", 0, "t2.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "c4.probe", "record_type": "CNAME", "priority": "0", "content": "t2.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=n/a (сценарий поведения), cleanup=verified

#### P9-txt1

- `zone/add_txt` `{"subdomain": "txt1.probe", "text": "regru probe with spaces"}` (bytes=23): **success**
- readback: `[["txt1.probe", "TXT", 0, "regru probe with spaces"]]`
- `zone/add_txt` `{"subdomain": "txt1.probe", "text": "control"}` (bytes=7): **success**
- readback: `[["txt1.probe", "TXT", 0, "control"], ["txt1.probe", "TXT", 0, "regru probe with spaces"]]`
- `zone/remove_record` `{"subdomain": "txt1.probe", "record_type": "TXT", "priority": "0", "content": "regru probe with spaces"}`: **success**
- readback: `[["txt1.probe", "TXT", 0, "control"]]`
- `zone/remove_record` (уборка) `{"subdomain": "txt1.probe", "record_type": "TXT", "priority": "0", "content": "control"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P9-txt2

- `zone/add_txt` `{"subdomain": "txt2.probe", "text": "regru \"quoted\" probe"}` (bytes=20): **success**
- readback: `[["txt2.probe", "TXT", 0, "regru \"quoted\" probe"]]`
- `zone/add_txt` `{"subdomain": "txt2.probe", "text": "control"}` (bytes=7): **success**
- readback: `[["txt2.probe", "TXT", 0, "control"], ["txt2.probe", "TXT", 0, "regru \"quoted\" probe"]]`
- `zone/remove_record` `{"subdomain": "txt2.probe", "record_type": "TXT", "priority": "0", "content": "regru \"quoted\" probe"}`: **success**
- readback: `[["txt2.probe", "TXT", 0, "control"]]`
- `zone/remove_record` (уборка) `{"subdomain": "txt2.probe", "record_type": "TXT", "priority": "0", "content": "control"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P9-txt3

- `zone/add_txt` `{"subdomain": "txt3.probe", "text": "regru back\\slash probe"}` (bytes=22): **success**
- readback: `[["txt3.probe", "TXT", 0, "regru back\\slash probe"]]`
- `zone/add_txt` `{"subdomain": "txt3.probe", "text": "control"}` (bytes=7): **success**
- readback: `[["txt3.probe", "TXT", 0, "control"], ["txt3.probe", "TXT", 0, "regru back\\slash probe"]]`
- `zone/remove_record` `{"subdomain": "txt3.probe", "record_type": "TXT", "priority": "0", "content": "regru back\\slash probe"}`: **success**
- readback: `[["txt3.probe", "TXT", 0, "control"]]`
- `zone/remove_record` (уборка) `{"subdomain": "txt3.probe", "record_type": "TXT", "priority": "0", "content": "control"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P9-txt4

- `zone/add_txt` `{"subdomain": "txt4.probe", "text": "v=probe1; k=regru; p=test"}` (bytes=25): **success**
- readback: `[["txt4.probe", "TXT", 0, "v=probe1; k=regru; p=test"]]`
- `zone/add_txt` `{"subdomain": "txt4.probe", "text": "control"}` (bytes=7): **success**
- readback: `[["txt4.probe", "TXT", 0, "control"], ["txt4.probe", "TXT", 0, "v=probe1; k=regru; p=test"]]`
- `zone/remove_record` `{"subdomain": "txt4.probe", "record_type": "TXT", "priority": "0", "content": "v=probe1; k=regru; p=test"}`: **success**
- readback: `[["txt4.probe", "TXT", 0, "control"]]`
- `zone/remove_record` (уборка) `{"subdomain": "txt4.probe", "record_type": "TXT", "priority": "0", "content": "control"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

Финал r1018a: тестовых записей осталось 0, зона вне префикса равна baseline: True, SOA до {'minimum_ttl': '3h', 'ttl': '1d'} после {'minimum_ttl': '3h', 'ttl': '1d'}.

### Запуск r1018b

#### P9-txt5

- `zone/add_txt` `{"subdomain": "txt5.probe", "text": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` (bytes=255): **success**
- readback: `[["txt5.probe", "TXT", 0, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]]`
- `zone/add_txt` `{"subdomain": "txt5.probe", "text": "control"}` (bytes=7): **success**
- readback: `[["txt5.probe", "TXT", 0, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"], ["txt5.probe", "TXT", 0, "control"]]`
- `zone/remove_record` `{"subdomain": "txt5.probe", "record_type": "TXT", "priority": "0", "content": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`: **success**
- readback: `[["txt5.probe", "TXT", 0, "control"]]`
- `zone/remove_record` (уборка) `{"subdomain": "txt5.probe", "record_type": "TXT", "priority": "0", "content": "control"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P9-txt6

- `zone/add_txt` `{"subdomain": "txt6.probe", "text": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}` (bytes=256): **success**
- readback: `[["txt6.probe", "TXT", 0, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]]`
- `zone/add_txt` `{"subdomain": "txt6.probe", "text": "control"}` (bytes=7): **success**
- readback: `[["txt6.probe", "TXT", 0, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"], ["txt6.probe", "TXT", 0, "control"]]`
- `zone/remove_record` `{"subdomain": "txt6.probe", "record_type": "TXT", "priority": "0", "content": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`: **success**
- readback: `[["txt6.probe", "TXT", 0, "control"]]`
- `zone/remove_record` (уборка) `{"subdomain": "txt6.probe", "record_type": "TXT", "priority": "0", "content": "control"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P9-txt7

- `zone/add_txt` `{"subdomain": "txt7.probe", "text": "проверка regru"}` (bytes=22): **domain error TEXT_CONTAINS_INVALID_CHARACTERS**
- readback: `[]`
- `zone/add_txt` `{"subdomain": "txt7.probe", "text": "control"}` (bytes=7): **success**
- readback: `[["txt7.probe", "TXT", 0, "control"]]`
- `zone/remove_record` (уборка) `{"subdomain": "txt7.probe", "record_type": "TXT", "priority": "0", "content": "control"}`: **success**
- readback: `[]`

Итог: exact-delete=not-tested, cleanup=verified

#### P10

- `zone/add_aaaa` `{"subdomain": "aaaa.probe", "ipaddr": "2001:db8::1"}`: **success**
- readback: `[["aaaa.probe", "AAAA", null, "2001:db8::1"]]`
- `zone/add_aaaa` `{"subdomain": "aaaa.probe", "ipaddr": "2001:db8::2"}`: **success**
- readback: `[["aaaa.probe", "AAAA", null, "2001:db8::1"], ["aaaa.probe", "AAAA", null, "2001:db8::2"]]`
- `zone/remove_record` `{"subdomain": "aaaa.probe", "record_type": "AAAA", "priority": "None", "content": "2001:db8::1"}`: **success**
- readback: `[["aaaa.probe", "AAAA", null, "2001:db8::2"]]`
- `zone/remove_record` (уборка) `{"subdomain": "aaaa.probe", "record_type": "AAAA", "priority": "None", "content": "2001:db8::2"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

#### P11

- `zone/add_ns` `{"subdomain": "ns.probe", "dns_server": "ns1.example.net."}`: **success**
- readback: `[["ns.probe", "NS", 0, "ns1.example.net."]]`
- `zone/add_ns` `{"subdomain": "ns.probe", "dns_server": "ns2.example.net."}`: **success**
- readback: `[["ns.probe", "NS", 0, "ns1.example.net."], ["ns.probe", "NS", 0, "ns2.example.net."]]`
- `zone/remove_record` `{"subdomain": "ns.probe", "record_type": "NS", "priority": "0", "content": "ns1.example.net."}`: **success**
- readback: `[["ns.probe", "NS", 0, "ns2.example.net."]]`
- `zone/remove_record` (уборка) `{"subdomain": "ns.probe", "record_type": "NS", "priority": "0", "content": "ns2.example.net."}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

Финал r1018b: тестовых записей осталось 0, зона вне префикса равна baseline: True, SOA до {'minimum_ttl': '3h', 'ttl': '1d'} после {'minimum_ttl': '3h', 'ttl': '1d'}.

### Запуск r1018c (проверка селектора AAAA с priority "0")

#### P10

- `zone/add_aaaa` `{"subdomain": "aaaa.probe", "ipaddr": "2001:db8::1"}`: **success**
- readback: `[["aaaa.probe", "AAAA", null, "2001:db8::1"]]`
- `zone/add_aaaa` `{"subdomain": "aaaa.probe", "ipaddr": "2001:db8::2"}`: **success**
- readback: `[["aaaa.probe", "AAAA", null, "2001:db8::1"], ["aaaa.probe", "AAAA", null, "2001:db8::2"]]`
- `zone/remove_record` `{"subdomain": "aaaa.probe", "record_type": "AAAA", "priority": "0", "content": "2001:db8::1"}`: **success**
- readback: `[["aaaa.probe", "AAAA", null, "2001:db8::2"]]`
- `zone/remove_record` (уборка) `{"subdomain": "aaaa.probe", "record_type": "AAAA", "priority": "0", "content": "2001:db8::2"}`: **success**
- readback: `[]`

Итог: exact-delete=pass, cleanup=verified

Финал r1018c: тестовых записей осталось 0, зона вне префикса равна baseline: True, SOA до {'minimum_ttl': '3h', 'ttl': '1d'} после {'minimum_ttl': '3h', 'ttl': '1d'}.
