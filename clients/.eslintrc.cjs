/* eslint-env node */
module.exports = {
  root: true,
  env: { browser: true, es2022: true, node: true },
  parser: '@typescript-eslint/parser',
  parserOptions: {
    ecmaVersion: 2022,
    sourceType: 'module',
    ecmaFeatures: { jsx: true },
  },
  plugins: ['@typescript-eslint', 'react', 'react-hooks'],
  extends: [
    'eslint:recommended',
    'plugin:react/recommended',
    'plugin:react-hooks/recommended',
  ],
  settings: { react: { version: 'detect' } },
  rules: {
    '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
    'react/react-in-jsx-scope': 'off',
    'react/prop-types': 'off',
    // Agent output is a prompt-injection and XSS vector. These rules are the
    // static half of the chokepoint; PlainText is the runtime half.
    'react/no-danger': 'error',
    'no-restricted-syntax': [
      'error',
      {
        selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
        message: 'Agent output is untrusted; render it with PlainText.',
      },
      {
        selector: "MemberExpression[property.name='innerHTML']",
        message: 'innerHTML is banned; agent output is untrusted.',
      },
      {
        selector: "MemberExpression[property.name='outerHTML']",
        message: 'outerHTML is banned; agent output is untrusted.',
      },
      {
        selector: "CallExpression[callee.name='eval']",
        message: 'eval is banned.',
      },
      {
        selector: "NewExpression[callee.name='Function']",
        message: 'The Function constructor is banned.',
      },
      {
        selector: "CallExpression[callee.object.name='window'][callee.property.name='open']",
        message: 'window.open is banned.',
      },
    ],
    'no-restricted-imports': [
      'error',
      {
        patterns: [
          {
            group: [
              'react-markdown',
              'markdown-it',
              'marked',
              'remark*',
              'rehype*',
              'dompurify',
              'sanitize-html',
              'xss',
            ],
            message:
              'Markdown renderers and HTML sanitizers are banned; render agent output as plain text.',
          },
        ],
      },
    ],
  },
  overrides: [
    {
      files: ['*.config.*', '*.cjs', 'vite.config.ts'],
      env: { node: true },
      rules: { 'no-restricted-syntax': 'off' },
    },
    {
      files: ['**/test/**', '**/*.test.ts', '**/*.test.tsx'],
      env: { node: true },
    },
  ],
  ignorePatterns: ['dist', 'node_modules', '*.d.ts', 'coverage'],
};
