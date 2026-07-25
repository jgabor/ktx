.mode json

-- This query emits only bounded metadata, hashes, exact sizes, deterministic
-- lexical probes, and character-based estimates. It never emits call content.
WITH selected(id, ordinal) AS (
  VALUES
    ('mjdxjpmddqazywuy', 1),
    ('bucunrqhxszwijyd', 2),
    ('mhtkmnclftrcixqz', 3),
    ('knwqasghrqjygpzt', 4),
    ('zhbmruwpmmglxlfk', 5),
    ('rtwiwhlavvxamsdq', 6)
)
SELECT
  tc.id,
  tc.tool,
  CASE
    WHEN json_valid(tc.input)
      AND typeof(json_extract(tc.input, '$.command')) = 'text'
    THEN CASE
      WHEN instr(trim(json_extract(tc.input, '$.command')), ' ') > 0
      THEN substr(
        trim(json_extract(tc.input, '$.command')),
        1,
        instr(trim(json_extract(tc.input, '$.command')), ' ') - 1
      )
      ELSE trim(json_extract(tc.input, '$.command'))
    END
    ELSE ''
  END AS command_binary,
  tc.content_hash AS content_sha256,
  tc.timestamp,
  tc.runtime,
  length(tc.output) AS output_chars,
  (length(tc.output) - length(replace(tc.output, char(10), '')))
    + CASE
      WHEN length(tc.output) > 0 AND substr(tc.output, -1) != char(10) THEN 1
      ELSE 0
    END AS lines,
  json_valid(tc.output) AS json_document,
  (
    instr(tc.output, 'diff --git ') > 0
    OR (
      instr(tc.output, char(10) || '--- ') > 0
      AND instr(tc.output, char(10) || '+++ ') > 0
    )
  ) AS diff_markers,
  (instr(tc.output, '[debug]') > 0 AND instr(tc.output, '[error]') > 0)
    AS log_markers,
  (
    instr(tc.output, '# ') > 0
    AND (instr(tc.output, 'MUST') > 0 OR instr(tc.output, 'must') > 0)
  ) AS instruction_markers,
  tc.is_error,
  tc.tokens_in,
  tc.tokens_out,
  tc.cost
FROM selected
JOIN tool_calls tc ON tc.id = selected.id
ORDER BY selected.ordinal;

WITH rows AS (
  SELECT
    tc.tool,
    CASE
      WHEN json_valid(tc.input)
        AND typeof(json_extract(tc.input, '$.command')) = 'text'
      THEN trim(json_extract(tc.input, '$.command'))
      ELSE ''
    END AS command_text,
    (length(tc.output) + 3) / 4 AS raw_tokens,
    (
      coalesce(
        (
          SELECT length(c.output)
          FROM compressed c
          WHERE c.source_id = tc.id
            AND c.timestamp <= '2026-07-25T03:34:27.68361733Z'
          ORDER BY c.timestamp DESC
          LIMIT 1
        ),
        length(tc.output)
      ) + 3
    ) / 4 AS delivered_tokens
  FROM tool_calls tc
  WHERE tc.output IS NOT NULL
    AND tc.timestamp <= '2026-07-25T03:35:14.591422717Z'
), grouped AS (
  SELECT
    tool,
    CASE
      WHEN instr(command_text, ' ') > 0
      THEN substr(command_text, 1, instr(command_text, ' ') - 1)
      ELSE command_text
    END AS command,
    count(*) AS calls,
    sum(raw_tokens) AS raw_estimated_tokens,
    sum(delivered_tokens) AS delivered_estimated_tokens
  FROM rows
  GROUP BY tool, command
  HAVING calls >= 2 AND delivered_estimated_tokens >= 50
)
SELECT *
FROM grouped
ORDER BY delivered_estimated_tokens DESC, tool ASC, command ASC
LIMIT 6;
