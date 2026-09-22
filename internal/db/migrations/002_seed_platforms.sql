-- 002_seed_platforms.sql: stable-id seed rows for platforms (job boards 1-9, application systems 10+).

INSERT OR IGNORE INTO platforms (id, name, platform_type, base_url_pattern) VALUES
    (1, 'remoteok',        'job_board',          'https://remoteok.com/remote-dev-jobs'),
    (2, 'remotive',        'job_board',          'https://remotive.com/remote-jobs/software-dev'),
    (3, 'himalayas',       'job_board',          'https://himalayas.app/jobs'),
    (4, 'weworkremotely',  'job_board',          'https://weworkremotely.com/categories/remote-programming-jobs'),
    (10, 'greenhouse',     'application_system', 'https://boards.greenhouse.io/%s'),
    (11, 'lever',          'application_system', 'https://jobs.lever.co/%s'),
    (12, 'workday',        'application_system', 'https://%s.myworkdayjobs.com/%s'),
    (13, 'icims',          'application_system', 'https://%s.icims.com/jobs'),
    (14, 'ashby',          'application_system', 'https://jobs.ashbyhq.com/%s'),
    (15, 'smartrecruiters','application_system', 'https://jobs.smartrecruiters.com/%s'),
    (16, 'custom',         'application_system', NULL);
